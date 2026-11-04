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
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"meow.blue/meow/message"
	"meow.blue/meow/server/internal/ids"
)

//go:embed schema.sql
var schemaSQL string

//go:embed gateway.sql
var gatewaySQL string

//go:embed sync.sql
var syncSQL string

const schemaVersion = 9

var (
	ErrNotFound            = errors.New("not found")
	ErrGatewayRouteChanged = errors.New("gateway route changed before admission")
	ErrResolved            = errors.New("question already resolved")
	ErrExpired             = errors.New("question expired")
	ErrInvalidAnswer       = errors.New("answer is not one of the allowed choices")
	ErrIdempotencyMismatch = errors.New("idempotency key reused with a different request")
)

// Limits caps what one device can create; zero means unlimited. They keep a
// public server usable when a Device Key leaks or an agent loops.
type Limits struct {
	KeysPerDevice    int // active (unrevoked) Device Keys
	PendingQuestions int // questions still waiting for an answer
	DailyMessages    int // notifications and questions in any 24 hours
}

// QuotaError reports which limit a request would exceed.
type QuotaError struct {
	Limit string // "keys", "pending_questions" or "daily_messages"
	Max   int
}

func (e *QuotaError) Error() string {
	switch e.Limit {
	case "keys":
		return fmt.Sprintf("this device already has %d active Device Keys; revoke one first", e.Max)
	case "pending_questions":
		return fmt.Sprintf("this device already has %d unanswered questions; answer or cancel some first", e.Max)
	default:
		return fmt.Sprintf("this device has reached its limit of %d messages in 24 hours", e.Max)
	}
}

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
)

var DefaultScopes = []string{ScopeNotify}

type Store struct {
	Limits Limits
	w      *sql.DB
	r      *sql.DB
	// Now is the clock used for all timestamps; tests may override it.
	Now func() time.Time
}

func Open(path string) (*Store, error) {
	if strings.HasPrefix(path, "file:") || path == ":memory:" {
		return nil, fmt.Errorf("database must use a filesystem path")
	}
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := privateDatabaseFile(path, true); err != nil {
		return nil, fmt.Errorf("prepare private database: %w", err)
	}
	if err := privateSidecars(path); err != nil {
		return nil, err
	}
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
	if err := privateSidecars(path); err != nil {
		s.Close()
		return nil, err
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
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
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
		if v == 0 {
			if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
				return err
			}
		}
		if v < 2 {
			if _, err := tx.ExecContext(ctx, gatewaySQL); err != nil {
				return err
			}
		}
		if v < 3 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE notifications ADD COLUMN context TEXT`); err != nil {
				return err
			}
		}
		if v < 4 {
			if _, err := tx.ExecContext(ctx, receiptSchema); err != nil {
				return err
			}
			// Preserve quota usage when upgrading an existing message database.
			if _, err := tx.ExecContext(ctx, `INSERT INTO notification_receipts
				SELECT id, device_id, created_at FROM notifications WHERE created_at > ?
				ON CONFLICT(notification_id) DO NOTHING`, ms(s.now().Add(-24*time.Hour))); err != nil {
				return err
			}
		}
		if v < 5 {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE notifications ADD COLUMN event TEXT NOT NULL DEFAULT '' CHECK(event IN ('','result','attention','failure'))`); err != nil {
				return err
			}
		}
		if v < 6 {
			if _, err := tx.ExecContext(ctx, performanceIndexes); err != nil {
				return err
			}
		}
		if v < 7 {
			if _, err := tx.ExecContext(ctx, gatewayQuotaSchema); err != nil {
				return err
			}
			// Preserve active usage from every old route sharing a token. Taking
			// the latest start is conservative across staggered legacy windows.
			if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_push_quotas(apns_token,apns_env,quota_at,quota_count)
 SELECT lower(apns_token),apns_env,MAX(quota_at),SUM(quota_count) FROM gateway_routes
 WHERE quota_at>? GROUP BY lower(apns_token),apns_env
 ON CONFLICT(apns_token,apns_env) DO UPDATE SET quota_at=MAX(quota_at,excluded.quota_at),quota_count=MAX(quota_count,excluded.quota_count)`, ms(s.now().Add(-time.Minute))); err != nil {
				return err
			}
		}
		if v < 8 {
			if _, err := tx.ExecContext(ctx, syncSQL); err != nil {
				return err
			}
		}
		if v < 9 {
			if _, err := tx.ExecContext(ctx, lifecycleSchema); err != nil {
				return err
			}
			// Existing routes get a complete inactivity window at upgrade;
			// neither their secrets nor their independent quotas are replaced.
			if _, err := tx.ExecContext(ctx, `UPDATE gateway_routes SET created_at=?,last_seen_at=?`, ms(s.now()), ms(s.now())); err != nil {
				return err
			}
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
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".meow-backup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	snapshot := filepath.Join(stage, "snapshot.db")
	if err = privateDatabaseFile(snapshot, true); err != nil {
		return err
	}
	if _, err = s.r.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return err
	}
	// Publish the complete snapshot atomically without replacing any existing
	// file or following a destination symlink created during the backup.
	return os.Link(snapshot, path)
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

// An APNs token reaches one app install, which holds one registration at a time.
// Claiming a token removes it from older registrations, so their remaining
// Device Keys can't keep waking the phone with pushes it can no longer open.
func (s *Store) UpdateDevice(ctx context.Context, id string, u DeviceUpdate) (Device, error) {
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if u.APNsToken != nil && *u.APNsToken != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE devices SET apns_token = NULL
				WHERE id != ? AND lower(apns_token) = lower(?)`, id, *u.APNsToken); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `UPDATE devices SET
			name       = COALESCE(?1, name),
			apns_token = CASE WHEN ?2 IS NULL THEN apns_token ELSE NULLIF(?2, '') END,
			apns_env   = COALESCE(?3, apns_env)
			WHERE id = ?4`, u.Name, u.APNsToken, u.APNsEnv, id)
		return err
	})
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
	k.Scopes = []string{}
	for _, scope := range strings.Split(scopes, ",") {
		if scope == ScopeNotify {
			k.Scopes = append(k.Scopes, scope)
		}
	}
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
		if err := s.checkQuota(ctx, tx, "keys", s.Limits.KeysPerDevice,
			`SELECT COUNT(*) FROM api_keys WHERE device_id = ? AND revoked_at IS NULL`, deviceID); err != nil {
			return err
		}
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
		`DELETE FROM api_keys WHERE id = ? AND device_id = ? AND revoked_at IS NULL`, keyID, deviceID)
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
	Event       message.EventKind
	Context     *message.Context
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

const notificationColumns = `n.id, n.device_id, COALESCE(n.key_id, ''), n.type, n.title, n.body, n.source, n.priority,
	COALESCE(n.question_id, ''), n.push_status, COALESCE(n.push_error, ''), n.created_at, n.delivered_at, n.read_at,
	q.status, q.answer, q.choices, q.expires_at, n.context, n.event`
const notificationFrom = `FROM notifications n LEFT JOIN questions q ON q.id = n.question_id`
const notificationSelect = `SELECT ` + notificationColumns + ` ` + notificationFrom

func scanNotification(row scanner) (Notification, error) {
	var n Notification
	var created int64
	var delivered, read, qExpires sql.NullInt64
	var qStatus, qAnswer, qChoices, origin sql.NullString
	if err := row.Scan(&n.ID, &n.DeviceID, &n.KeyID, &n.Type, &n.Title, &n.Body, &n.Source, &n.Priority,
		&n.QuestionID, &n.PushStatus, &n.PushError, &created, &delivered, &read,
		&qStatus, &qAnswer, &qChoices, &qExpires, &origin, &n.Event); err != nil {
		return n, noRows(err)
	}
	if origin.Valid {
		if err := json.Unmarshal([]byte(origin.String), &n.Context); err != nil {
			return n, err
		}
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
	Event    message.EventKind
	Context  *message.Context
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
	var origin any
	if n.Context != nil {
		// Context is a closed struct of strings; JSON encoding cannot fail.
		b, _ := json.Marshal(n.Context)
		origin = string(b)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO notifications
		(id, device_id, key_id, type, title, body, source, priority, question_id, push_status, created_at, context, event)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.DeviceID, n.KeyID, n.Type, n.Title, n.Body, n.Source, n.Priority, qid, n.PushStatus, ms(n.CreatedAt), origin, n.Event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_receipts (notification_id, device_id, created_at) VALUES (?, ?, ?)`,
		n.ID, n.DeviceID, ms(n.CreatedAt))
	return err
}

func newNotification(nn NewNotification, now time.Time) Notification {
	return Notification{
		Event: nn.Event, ID: ids.New("ntf"), DeviceID: nn.DeviceID, KeyID: nn.KeyID, Type: nn.Type, Title: nn.Title,
		Body: nn.Body, Source: nn.Source, Priority: nn.Priority, Context: nn.Context, PushStatus: PushQueued, CreatedAt: now,
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
		if err := s.checkDaily(ctx, tx, nn.DeviceID, now); err != nil {
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

func (s *Store) inboxQuery(deviceID string, iq InboxQuery) (string, []any) {
	at, id := int64(math.MaxInt64), ""
	if !iq.After.At.IsZero() {
		at, id = ms(iq.After.At), iq.After.ID
	}
	where := ` WHERE n.device_id = ? AND (n.created_at, n.id) < (?, ?)`
	args := []any{deviceID, at, id}
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
	return notificationSelect + where + ` ORDER BY n.created_at DESC, n.id DESC LIMIT ?`, args
}

// Inbox lists a device's notifications newest first, strictly after the cursor.
func (s *Store) Inbox(ctx context.Context, deviceID string, iq InboxQuery) ([]Notification, error) {
	query, args := s.inboxQuery(deviceID, iq)
	rows, err := s.r.QueryContext(ctx, query, args...)
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
	return PushTarget{Notification: n, Device: d}, nil
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

func (s *Store) Question(ctx context.Context, id string) (Question, error) {
	return scanQuestion(s.r.QueryRowContext(ctx, questionSelect+` WHERE q.id = ?`, id))
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

// checkQuota fails with a QuotaError when the count query already reaches max.
// It runs inside the write transaction, so concurrent requests can't overshoot.
func (s *Store) checkQuota(ctx context.Context, tx *sql.Tx, limit string, max int, query string, args ...any) error {
	if max <= 0 {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return err
	}
	if n >= max {
		return &QuotaError{Limit: limit, Max: max}
	}
	return nil
}

// checkDaily limits notifications, including the ones questions create, over
// a sliding 24 hours, independently of body retention and explicit deletion.
func (s *Store) checkDaily(ctx context.Context, tx *sql.Tx, deviceID string, now time.Time) error {
	return s.checkQuota(ctx, tx, "daily_messages", s.Limits.DailyMessages,
		`SELECT COUNT(*) FROM notification_receipts WHERE device_id = ? AND created_at > ?`,
		deviceID, ms(now.Add(-24*time.Hour)))
}

// PruneInactiveDevices deletes devices not seen for longer than idle, with
// their keys, notifications and questions. A device is seen whenever the app
// or its notification extension talks to the server.
func (s *Store) PruneInactiveDevices(ctx context.Context, idle time.Duration) (int64, error) {
	res, err := s.w.ExecContext(ctx, `DELETE FROM devices WHERE last_seen_at < ?`, ms(s.now().Add(-idle)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Prune deletes resolved questions and notifications older than retention,
// previously revoked credentials, and quota/idempotency metadata after 24h.
// Pending questions and independent notification quota receipts are preserved.
// Each bounded batch is atomic; earlier batches remain committed if a later one fails.
func (s *Store) Prune(ctx context.Context, retention time.Duration) (int64, error) {
	now := s.now()
	cutoff := ms(now.Add(-retention))
	var total int64
	for {
		batch, err := s.pruneBatch(ctx, cutoff, ms(now.Add(-24*time.Hour)))
		total += batch
		if err != nil || batch == 0 {
			return total, err
		}
	}
}

func (s *Store) pruneBatch(ctx context.Context, cutoff, metadataCutoff int64) (int64, error) {
	var total int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range []struct {
			sql  string
			args []any
		}{
			{`DELETE FROM api_keys WHERE rowid IN (SELECT rowid FROM api_keys WHERE revoked_at IS NOT NULL ORDER BY revoked_at,rowid LIMIT 1000)`, nil},
			{`DELETE FROM questions WHERE rowid IN (SELECT rowid FROM questions WHERE status != 'pending' AND created_at <= ? ORDER BY created_at, rowid LIMIT 1000)`, []any{cutoff}},
			{`DELETE FROM notifications WHERE rowid IN (SELECT rowid FROM notifications WHERE question_id IS NULL AND created_at <= ? ORDER BY created_at, rowid LIMIT 1000)`, []any{cutoff}},
			// Keep metadata for the full retry window even after content expires.
			{`DELETE FROM idempotency WHERE rowid IN (SELECT rowid FROM idempotency WHERE created_at <= ? ORDER BY created_at, rowid LIMIT 1000)`, []any{metadataCutoff}},
			{`DELETE FROM notification_receipts WHERE rowid IN (SELECT rowid FROM notification_receipts WHERE created_at <= ? ORDER BY created_at, rowid LIMIT 1000)`, []any{metadataCutoff}},
		} {
			res, err := tx.ExecContext(ctx, stmt.sql, stmt.args...)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += n
		}
		return nil
	})
	if err != nil {
		return 0, err // the entire batch rolled back
	}
	return total, err
}
