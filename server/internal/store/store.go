// Package store persists Meow state in a single SQLite database (WAL mode).
//
// All writes go through one connection opened with BEGIN IMMEDIATE so that
// transactions are short and serialized; reads use a separate small pool.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"meow.blue/meow/server/internal/ids"
)

//go:embed schema.sql
var schemaSQL string

const schemaVersion = 1

var (
	ErrNotFound            = errors.New("not found")
	ErrResolved            = errors.New("question already resolved")
	ErrExpired             = errors.New("question expired")
	ErrInvalidAnswer       = errors.New("answer is not one of the allowed choices")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with a different request")
)

// Question statuses.
const (
	StatusPending   = "pending"
	StatusAnswered  = "answered"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
	StatusCancelled = "cancelled"
)

// Push statuses. "accepted" means APNs accepted the request; it is not proof of display.
const (
	PushQueued   = "queued"
	PushAccepted = "accepted"
	PushFailed   = "failed"
	PushSkipped  = "skipped"
)

// API key scopes.
const (
	ScopeNotify = "notify"
	ScopeAsk    = "ask"
)

var DefaultScopes = []string{ScopeNotify, ScopeAsk}

type Store struct {
	w *sql.DB
	r *sql.DB
	// Now is the clock used for all timestamps; tests may override it.
	Now func() time.Time
}

func Open(path string) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	s := &Store{w: w, r: r, Now: time.Now}
	if err := s.migrate(context.Background()); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return s, nil
}

func dsn(path string, writer bool) string {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(ON)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	if writer {
		q.Set("_txlock", "immediate")
	}
	return path + "?" + q.Encode()
}

func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.w.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	switch {
	case v == schemaVersion:
		return nil
	case v > schemaVersion:
		return fmt.Errorf("database schema v%d is newer than this server supports (v%d)", v, schemaVersion)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
		return err
	})
}

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Ping checks that the database is readable.
func (s *Store) Ping(ctx context.Context) error { return s.r.PingContext(ctx) }

// Backup writes a consistent snapshot of the live database to path using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, path string) error {
	_, err := s.r.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

func (s *Store) now() time.Time { return s.Now().UTC().Truncate(time.Millisecond) }

func ms(t time.Time) int64 { return t.UnixMilli() }

func fromMS(v int64) time.Time { return time.UnixMilli(v).UTC() }

func fromNullMS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMS(v.Int64)
	return &t
}

// Hash returns the stored form of a bearer secret.
func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

func hint(secret string) string {
	if len(secret) < 14 {
		return "…"
	}
	return secret[:9] + "…" + secret[len(secret)-4:]
}

type scanner interface{ Scan(...any) error }

func noRows(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// ---- devices ----

type Device struct {
	ID         string
	Name       string
	APNsToken  string
	APNsEnv    string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

const deviceCols = `id, name, COALESCE(apns_token, ''), apns_env, created_at, last_seen_at`

func scanDevice(row scanner) (Device, error) {
	var d Device
	var created, seen int64
	if err := row.Scan(&d.ID, &d.Name, &d.APNsToken, &d.APNsEnv, &created, &seen); err != nil {
		return d, noRows(err)
	}
	d.CreatedAt, d.LastSeenAt = fromMS(created), fromMS(seen)
	return d, nil
}

// Registration is returned once when an anonymous device registers.
// The secrets are never stored in plaintext and cannot be retrieved later.
type Registration struct {
	Device      Device
	DeviceToken string
	Key         APIKey
	KeySecret   string
}

func (s *Store) RegisterDevice(ctx context.Context, name string) (Registration, error) {
	now := s.now()
	reg := Registration{
		DeviceToken: ids.Secret("mdt"),
		Device:      Device{ID: ids.New("dev"), Name: name, APNsEnv: "production", CreatedAt: now, LastSeenAt: now},
	}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO devices (id, token_hash, name, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
			reg.Device.ID, Hash(reg.DeviceToken), name, ms(now), ms(now))
		if err != nil {
			return err
		}
		reg.Key, reg.KeySecret, err = insertKey(ctx, tx, reg.Device.ID, "Default", DefaultScopes, now)
		return err
	})
	return reg, err
}

func (s *Store) DeviceByToken(ctx context.Context, token string) (Device, error) {
	d, err := scanDevice(s.r.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE token_hash = ?`, Hash(token)))
	if err != nil {
		return d, err
	}
	if now := s.now(); now.Sub(d.LastSeenAt) > time.Minute {
		if _, err := s.w.ExecContext(ctx, `UPDATE devices SET last_seen_at = ? WHERE id = ?`, ms(now), d.ID); err == nil {
			d.LastSeenAt = now
		}
	}
	return d, nil
}

func (s *Store) Device(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.r.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE id = ?`, id))
}

// DeviceUpdate holds optional fields; nil leaves a field unchanged and an empty APNsToken clears it.
type DeviceUpdate struct {
	Name      *string
	APNsToken *string
	APNsEnv   *string
}

func (s *Store) UpdateDevice(ctx context.Context, id string, u DeviceUpdate) (Device, error) {
	_, err := s.w.ExecContext(ctx, `UPDATE devices SET
		name       = COALESCE(?1, name),
		apns_token = CASE WHEN ?2 IS NULL THEN apns_token ELSE NULLIF(?2, '') END,
		apns_env   = COALESCE(?3, apns_env)
		WHERE id = ?4`, u.Name, u.APNsToken, u.APNsEnv, id)
	if err != nil {
		return Device{}, err
	}
	return s.Device(ctx, id)
}

// ClearAPNsToken removes a token APNs reported as invalid, unless the device has already replaced it.
func (s *Store) ClearAPNsToken(ctx context.Context, deviceID, token string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE devices SET apns_token = NULL WHERE id = ? AND apns_token = ?`, deviceID, token)
	return err
}

// ---- API keys ----

type APIKey struct {
	ID         string
	DeviceID   string
	Hint       string
	Name       string
	Scopes     []string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

func (k APIKey) Has(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

const keyCols = `id, device_id, hint, name, scopes, created_at, last_used_at`

func scanKey(row scanner) (APIKey, error) {
	var k APIKey
	var scopes string
	var created int64
	var used sql.NullInt64
	if err := row.Scan(&k.ID, &k.DeviceID, &k.Hint, &k.Name, &scopes, &created, &used); err != nil {
		return k, noRows(err)
	}
	k.Scopes = strings.Split(scopes, ",")
	k.CreatedAt, k.LastUsedAt = fromMS(created), fromNullMS(used)
	return k, nil
}

func insertKey(ctx context.Context, tx *sql.Tx, deviceID, name string, scopes []string, now time.Time) (APIKey, string, error) {
	secret := ids.Secret("meow")
	k := APIKey{ID: ids.New("key"), DeviceID: deviceID, Hint: hint(secret), Name: name, Scopes: scopes, CreatedAt: now}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO api_keys (id, device_id, key_hash, hint, name, scopes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		k.ID, deviceID, Hash(secret), k.Hint, name, strings.Join(scopes, ","), ms(now))
	return k, secret, err
}

func (s *Store) CreateKey(ctx context.Context, deviceID, name string, scopes []string) (APIKey, string, error) {
	var k APIKey
	var secret string
	err := s.tx(ctx, func(tx *sql.Tx) (err error) {
		k, secret, err = insertKey(ctx, tx, deviceID, name, scopes, s.now())
		return err
	})
	return k, secret, err
}

func (s *Store) ListKeys(ctx context.Context, deviceID string) ([]APIKey, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT `+keyCols+` FROM api_keys WHERE device_id = ? AND revoked_at IS NULL ORDER BY created_at`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := []APIKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func (s *Store) RevokeKey(ctx context.Context, deviceID, keyID string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND device_id = ? AND revoked_at IS NULL`,
		ms(s.now()), keyID, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyBySecret authenticates a Device Key. Revoked keys are reported as ErrNotFound.
func (s *Store) KeyBySecret(ctx context.Context, secret string) (APIKey, error) {
	k, err := scanKey(s.r.QueryRowContext(ctx,
		`SELECT `+keyCols+` FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL`, Hash(secret)))
	if err != nil {
		return k, err
	}
	if now := s.now(); k.LastUsedAt == nil || now.Sub(*k.LastUsedAt) > time.Minute {
		if _, err := s.w.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, ms(now), k.ID); err == nil {
			k.LastUsedAt = &now
		}
	}
	return k, nil
}

// ---- idempotency ----

// Idem identifies a client retry. A zero Idem disables idempotency.
type Idem struct {
	Key         string
	RequestHash string
}

func lookupIdem(ctx context.Context, tx *sql.Tx, keyID string, idem Idem, resType string) (string, error) {
	if idem.Key == "" {
		return "", nil
	}
	var hash, typ, id string
	err := tx.QueryRowContext(ctx,
		`SELECT request_hash, resource_type, resource_id FROM idempotency WHERE key_id = ? AND idem_key = ?`,
		keyID, idem.Key).Scan(&hash, &typ, &id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	case hash != idem.RequestHash || typ != resType:
		return "", ErrIdempotencyMismatch
	}
	return id, nil
}

func saveIdem(ctx context.Context, tx *sql.Tx, keyID string, idem Idem, resType, resID string, now time.Time) error {
	if idem.Key == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency (key_id, idem_key, request_hash, resource_type, resource_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		keyID, idem.Key, idem.RequestHash, resType, resID, ms(now))
	return err
}

// ---- notifications ----

type Notification struct {
	ID          string
	DeviceID    string
	KeyID       string
	Type        string // info | alert | question
	Title       string
	Body        string
	Source      string
	Priority    string // low | normal | high
	QuestionID  string
	PushStatus  string
	PushError   string
	CreatedAt   time.Time
	DeliveredAt *time.Time
	ReadAt      *time.Time
	Question    *QuestionState // set when Type == "question"
}

type QuestionState struct {
	Status    string
	Answer    string
	Choices   []string
	ExpiresAt time.Time
}

const notificationSelect = `SELECT n.id, n.device_id, COALESCE(n.key_id, ''), n.type, n.title, n.body, n.source, n.priority,
	COALESCE(n.question_id, ''), n.push_status, COALESCE(n.push_error, ''), n.created_at, n.delivered_at, n.read_at,
	q.status, q.answer, q.choices, q.expires_at
	FROM notifications n LEFT JOIN questions q ON q.id = n.question_id`

func scanNotification(row scanner) (Notification, error) {
	var n Notification
	var created int64
	var delivered, read, qExpires sql.NullInt64
	var qStatus, qAnswer, qChoices sql.NullString
	if err := row.Scan(&n.ID, &n.DeviceID, &n.KeyID, &n.Type, &n.Title, &n.Body, &n.Source, &n.Priority,
		&n.QuestionID, &n.PushStatus, &n.PushError, &created, &delivered, &read,
		&qStatus, &qAnswer, &qChoices, &qExpires); err != nil {
		return n, noRows(err)
	}
	n.CreatedAt, n.DeliveredAt, n.ReadAt = fromMS(created), fromNullMS(delivered), fromNullMS(read)
	if qStatus.Valid {
		choices, err := decodeChoices(qChoices)
		if err != nil {
			return n, err
		}
		n.Question = &QuestionState{Status: qStatus.String, Answer: qAnswer.String, Choices: choices, ExpiresAt: fromMS(qExpires.Int64)}
	}
	return n, nil
}

type NewNotification struct {
	DeviceID string
	KeyID    string
	Type     string
	Title    string
	Body     string
	Source   string
	Priority string
}

func insertNotification(ctx context.Context, tx *sql.Tx, n Notification) error {
	var qid any
	if n.QuestionID != "" {
		qid = n.QuestionID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO notifications
		(id, device_id, key_id, type, title, body, source, priority, question_id, push_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.DeviceID, n.KeyID, n.Type, n.Title, n.Body, n.Source, n.Priority, qid, n.PushStatus, ms(n.CreatedAt))
	return err
}

func newNotification(nn NewNotification, now time.Time) Notification {
	return Notification{
		ID: ids.New("ntf"), DeviceID: nn.DeviceID, KeyID: nn.KeyID, Type: nn.Type, Title: nn.Title,
		Body: nn.Body, Source: nn.Source, Priority: nn.Priority, PushStatus: PushQueued, CreatedAt: now,
	}
}

// CreateNotification inserts an info/alert notification. replay is true when the
// idempotency key matched an earlier identical request and nothing new was created.
func (s *Store) CreateNotification(ctx context.Context, nn NewNotification, idem Idem) (n Notification, replay bool, err error) {
	now := s.now()
	var existing string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if existing, err = lookupIdem(ctx, tx, nn.KeyID, idem, "notification"); err != nil || existing != "" {
			return err
		}
		n = newNotification(nn, now)
		if err := insertNotification(ctx, tx, n); err != nil {
			return err
		}
		return saveIdem(ctx, tx, nn.KeyID, idem, "notification", n.ID, now)
	})
	if err != nil {
		return Notification{}, false, err
	}
	if existing != "" {
		n, err = s.Notification(ctx, existing)
		return n, true, err
	}
	return n, false, nil
}

func (s *Store) Notification(ctx context.Context, id string) (Notification, error) {
	return scanNotification(s.r.QueryRowContext(ctx, notificationSelect+` WHERE n.id = ?`, id))
}

// Cursor positions inbox pagination; the zero Cursor starts at the newest notification.
type Cursor struct {
	At time.Time
	ID string
}

// InboxQuery selects a page of a device's inbox.
type InboxQuery struct {
	Unread  bool   // only notifications not yet read
	Pending bool   // only questions still waiting for an answer
	Search  string // case-insensitive substring of title, body or source
	After   Cursor
	Limit   int
}

func likePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// Inbox lists a device's notifications newest first, strictly after the cursor.
func (s *Store) Inbox(ctx context.Context, deviceID string, iq InboxQuery) ([]Notification, error) {
	at, id := int64(math.MaxInt64), ""
	if !iq.After.At.IsZero() {
		at, id = ms(iq.After.At), iq.After.ID
	}
	where := ` WHERE n.device_id = ? AND (n.created_at < ? OR (n.created_at = ? AND n.id < ?))`
	args := []any{deviceID, at, at, id}
	if iq.Unread {
		where += ` AND n.read_at IS NULL`
	}
	if iq.Pending {
		where += ` AND q.status = 'pending' AND q.expires_at > ?`
		args = append(args, ms(s.now()))
	}
	if iq.Search != "" {
		where += ` AND (n.title LIKE ? ESCAPE '\' OR n.body LIKE ? ESCAPE '\' OR n.source LIKE ? ESCAPE '\')`
		p := likePattern(iq.Search)
		args = append(args, p, p, p)
	}
	args = append(args, iq.Limit)
	rows, err := s.r.QueryContext(ctx, notificationSelect+where+` ORDER BY n.created_at DESC, n.id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// InboxCounts returns how many notifications are unread and how many questions are still pending.
func (s *Store) InboxCounts(ctx context.Context, deviceID string) (unread, pending int, err error) {
	err = s.r.QueryRowContext(ctx, `SELECT
		COUNT(*) FILTER (WHERE n.read_at IS NULL),
		COUNT(*) FILTER (WHERE q.status = 'pending' AND q.expires_at > ?)
		FROM notifications n LEFT JOIN questions q ON q.id = n.question_id
		WHERE n.device_id = ?`, ms(s.now()), deviceID).Scan(&unread, &pending)
	return unread, pending, err
}

// MarkAllRead marks every unread notification of a device as read.
func (s *Store) MarkAllRead(ctx context.Context, deviceID string) (int64, error) {
	res, err := s.w.ExecContext(ctx, `UPDATE notifications SET read_at = ? WHERE device_id = ? AND read_at IS NULL`, ms(s.now()), deviceID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) MarkRead(ctx context.Context, deviceID, id string) error {
	return s.markTime(ctx, "read_at", deviceID, id)
}

func (s *Store) MarkDelivered(ctx context.Context, deviceID, id string) error {
	return s.markTime(ctx, "delivered_at", deviceID, id)
}

func (s *Store) markTime(ctx context.Context, col, deviceID, id string) error {
	res, err := s.w.ExecContext(ctx,
		`UPDATE notifications SET `+col+` = COALESCE(`+col+`, ?) WHERE id = ? AND device_id = ?`, ms(s.now()), id, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- push bookkeeping ----

type PushTarget struct {
	Notification Notification
	Device       Device
	Badge        int // unread notifications, shown on the app icon
}

func (s *Store) PushTarget(ctx context.Context, notificationID string) (PushTarget, error) {
	n, err := s.Notification(ctx, notificationID)
	if err != nil {
		return PushTarget{}, err
	}
	d, err := s.Device(ctx, n.DeviceID)
	if err != nil {
		return PushTarget{}, err
	}
	unread, _, err := s.InboxCounts(ctx, n.DeviceID)
	return PushTarget{Notification: n, Device: d, Badge: unread}, err
}

func (s *Store) SetPushStatus(ctx context.Context, id, status, errMsg string) error {
	_, err := s.w.ExecContext(ctx, `UPDATE notifications SET push_status = ?, push_error = NULLIF(?, '') WHERE id = ?`, status, errMsg, id)
	return err
}

// QueuedNotifications returns notifications still waiting for a push attempt created in [after, before).
func (s *Store) QueuedNotifications(ctx context.Context, after, before time.Time) ([]string, error) {
	rows, err := s.r.QueryContext(ctx,
		`SELECT id FROM notifications WHERE push_status = 'queued' AND created_at >= ? AND created_at < ? ORDER BY created_at`,
		ms(after), ms(before))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- questions ----

type Question struct {
	ID             string
	DeviceID       string
	KeyID          string
	Text           string
	Choices        []string // nil means free-text reply
	Status         string
	Answer         string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ResolvedAt     *time.Time
	NotificationID string
}

const questionSelect = `SELECT q.id, q.device_id, COALESCE(q.key_id, ''), COALESCE(n.title, ''), q.choices, q.status,
	COALESCE(q.answer, ''), q.created_at, q.expires_at, q.resolved_at, COALESCE(n.id, '')
	FROM questions q LEFT JOIN notifications n ON n.question_id = q.id`

func scanQuestion(row scanner) (Question, error) {
	var q Question
	var choices sql.NullString
	var created, expires int64
	var resolved sql.NullInt64
	if err := row.Scan(&q.ID, &q.DeviceID, &q.KeyID, &q.Text, &choices, &q.Status, &q.Answer,
		&created, &expires, &resolved, &q.NotificationID); err != nil {
		return q, noRows(err)
	}
	var err error
	q.Choices, err = decodeChoices(choices)
	q.CreatedAt, q.ExpiresAt, q.ResolvedAt = fromMS(created), fromMS(expires), fromNullMS(resolved)
	return q, err
}

func decodeChoices(v sql.NullString) ([]string, error) {
	if !v.Valid {
		return nil, nil
	}
	var c []string
	return c, json.Unmarshal([]byte(v.String), &c)
}

type NewQuestion struct {
	NewNotification // Title holds the question text
	Choices         []string
	TTL             time.Duration
}

func (s *Store) CreateQuestion(ctx context.Context, nq NewQuestion, idem Idem) (q Question, replay bool, err error) {
	now := s.now()
	var existing string
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if existing, err = lookupIdem(ctx, tx, nq.KeyID, idem, "question"); err != nil || existing != "" {
			return err
		}
		var choices any
		if nq.Choices != nil {
			b, _ := json.Marshal(nq.Choices)
			choices = string(b)
		}
		qid := ids.New("qst")
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO questions (id, device_id, key_id, choices, status, created_at, expires_at) VALUES (?, ?, ?, ?, 'pending', ?, ?)`,
			qid, nq.DeviceID, nq.KeyID, choices, ms(now), ms(now.Add(nq.TTL))); err != nil {
			return err
		}
		n := newNotification(nq.NewNotification, now)
		n.Type, n.QuestionID = "question", qid
		if err := insertNotification(ctx, tx, n); err != nil {
			return err
		}
		q.ID = qid
		return saveIdem(ctx, tx, nq.KeyID, idem, "question", qid, now)
	})
	if err != nil {
		return Question{}, false, err
	}
	if existing != "" {
		q, err = s.Question(ctx, existing)
		return q, true, err
	}
	q, err = s.Question(ctx, q.ID)
	return q, false, err
}

func (s *Store) Question(ctx context.Context, id string) (Question, error) {
	return scanQuestion(s.r.QueryRowContext(ctx, questionSelect+` WHERE q.id = ?`, id))
}

// matchChoice returns the canonical answer, or false if it is not allowed.
func matchChoice(choices []string, answer string) (string, bool) {
	if choices == nil {
		return answer, true
	}
	for _, c := range choices {
		if strings.EqualFold(c, answer) {
			return c, true
		}
	}
	return "", false
}

// Respond records the single accepted answer for a pending question.
//
// Retrying with the same idempotency key and answer returns the stored result
// with replay=true. Any other second response fails with ErrResolved. A
// response arriving after expiry marks the question expired and fails with
// ErrExpired; nothing is ever approved implicitly. On ErrResolved and
// ErrExpired the current question is still returned.
func (s *Store) Respond(ctx context.Context, questionID, deviceID, answer, idemKey string) (Question, bool, error) {
	var replay, expired bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		// Read the clock only once the write lock is held, so a response that
		// waited behind other writers is judged at the time it is applied.
		now := s.now()
		q, err := scanQuestion(tx.QueryRowContext(ctx, questionSelect+` WHERE q.id = ?`, questionID))
		if err != nil {
			return err
		}
		if q.DeviceID != deviceID {
			return ErrNotFound
		}
		canonical, valid := matchChoice(q.Choices, answer)

		var prevKey, prevAnswer string
		err = tx.QueryRowContext(ctx, `SELECT idempotency_key, answer FROM responses WHERE question_id = ?`, questionID).Scan(&prevKey, &prevAnswer)
		switch {
		case err == nil:
			if prevKey == idemKey && valid && prevAnswer == canonical {
				replay = true
				return nil
			}
			return ErrResolved
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}

		switch q.Status {
		case StatusPending:
		case StatusExpired:
			return ErrExpired
		default:
			return ErrResolved
		}
		if !now.Before(q.ExpiresAt) {
			expired = true
			_, err := tx.ExecContext(ctx, `UPDATE questions SET status = 'expired', resolved_at = ? WHERE id = ? AND status = 'pending'`, ms(now), questionID)
			return err
		}
		if !valid {
			return ErrInvalidAnswer
		}

		status := StatusAnswered
		if q.Choices != nil && strings.EqualFold(canonical, "reject") {
			status = StatusRejected
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE questions SET status = ?, answer = ?, resolved_at = ? WHERE id = ? AND status = 'pending'`,
			status, canonical, ms(now), questionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrResolved
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO responses (id, question_id, device_id, answer, idempotency_key, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ids.New("rsp"), questionID, deviceID, canonical, idemKey, ms(now)); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE notifications SET read_at = COALESCE(read_at, ?) WHERE question_id = ?`, ms(now), questionID)
		return err
	})
	if err != nil && !errors.Is(err, ErrResolved) && !errors.Is(err, ErrExpired) {
		return Question{}, false, err
	}
	if expired {
		err = ErrExpired
	}
	q, qerr := s.Question(ctx, questionID)
	if qerr != nil {
		return Question{}, false, qerr
	}
	return q, replay, err
}

// CancelQuestion cancels a pending question owned by deviceID.
func (s *Store) CancelQuestion(ctx context.Context, id, deviceID string) (Question, error) {
	res, err := s.w.ExecContext(ctx,
		`UPDATE questions SET status = 'cancelled', resolved_at = ? WHERE id = ? AND device_id = ? AND status = 'pending'`,
		ms(s.now()), id, deviceID)
	if err != nil {
		return Question{}, err
	}
	q, err := s.Question(ctx, id)
	if err != nil {
		return q, err
	}
	if q.DeviceID != deviceID {
		return Question{}, ErrNotFound
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return q, ErrResolved
	}
	return q, nil
}

// ExpireDue marks every pending question past its expiry as expired and returns their IDs.
func (s *Store) ExpireDue(ctx context.Context) ([]string, error) {
	rows, err := s.w.QueryContext(ctx,
		`UPDATE questions SET status = 'expired', resolved_at = ?1 WHERE status = 'pending' AND expires_at <= ?1 RETURNING id`,
		ms(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Prune deletes resolved questions and notifications older than retention, and
// idempotency records older than 24h. Pending questions are always kept.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	now := s.now()
	cutoff := ms(now.Add(-retention))
	var total int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []struct {
			sql string
			arg int64
		}{
			{`DELETE FROM questions WHERE status != 'pending' AND created_at < ?`, cutoff},
			{`DELETE FROM notifications WHERE question_id IS NULL AND created_at < ?`, cutoff},
			// Idempotency records must not outlive the resources they point to.
			{`DELETE FROM idempotency WHERE created_at < ?`, ms(now.Add(-min(retention, 24*time.Hour)))},
		} {
			res, err := tx.ExecContext(ctx, stmt.sql, stmt.arg)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += n
		}
		return nil
	})
	return total, err
}
