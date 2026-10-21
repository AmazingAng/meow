// Package client is a Go client for the Meow HTTP API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/message"
)

var ErrNoServer = errors.New("no Meow server configured; use the server address shown in the Meow app")

type Client struct {
	Server string // explicitly configured base URL
	Token  string // a Device Key (meow_…) or, for mobile calls, a device token (mdt_…)
	HTTP   *http.Client
}

func New(server, token string) *Client {
	return &Client{Server: server, Token: token, HTTP: &http.Client{Timeout: 90 * time.Second}}
}

// Error is an API error response.
type Error struct {
	Status   int
	Code     string
	Message  string
	Question *Question // the current question for 409 already_resolved and 410 expired
}

func (e *Error) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Message
}

// Temporary reports whether retrying the same request may succeed.
func (e *Error) Temporary() bool {
	if e.Code == "quota_exceeded" {
		return false // a device limit; retrying won't help until the user acts
	}
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

type Notification struct {
	Context     *message.Context `json:"context,omitempty"`
	ID          string           `json:"id"`
	Type        string           `json:"type"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	Source      string           `json:"source"`
	Priority    string           `json:"priority"`
	PushStatus  string           `json:"push_status"`
	PushError   string           `json:"push_error,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	DeliveredAt *time.Time       `json:"delivered_at"`
	ReadAt      *time.Time       `json:"read_at"`
	Question    *struct {
		ID        string    `json:"id"`
		Status    string    `json:"status"`
		Choices   []string  `json:"choices"`
		Answer    *string   `json:"answer"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"question,omitempty"`
}

type Question struct {
	ID             string     `json:"id"`
	Status         string     `json:"status"` // pending | answered | rejected | expired | cancelled
	Question       string     `json:"question"`
	Choices        []string   `json:"choices"`
	Answer         *string    `json:"answer"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      time.Time  `json:"expires_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	NotificationID string     `json:"notification_id"`
}

func (q *Question) Pending() bool { return q.Status == "pending" }

type Device struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	APNsEnv      string    `json:"apns_env"`
	HasAPNsToken bool      `json:"has_apns_token"`
	CreatedAt    time.Time `json:"created_at"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

type Key struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hint       string     `json:"hint"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	Secret     string     `json:"secret,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, idemKey string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	server := strings.TrimRight(c.Server, "/")
	if server == "" {
		return ErrNoServer
	}
	req, err := http.NewRequestWithContext(ctx, method, server+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	req.Header.Set("User-Agent", "meow-go-client")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Question *Question `json:"question"`
		}
		_ = json.Unmarshal(b, &e)
		return &Error{Status: resp.StatusCode, Code: e.Error.Code, Message: e.Error.Message, Question: e.Question}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.Unmarshal(b, out)
}

// ---- agent API (Device Key) ----

type NotifyRequest struct {
	Context  *message.Context `json:"context,omitempty"`
	Type     string           `json:"type,omitempty"` // info (default) | alert
	Title    string           `json:"title"`
	Body     string           `json:"body,omitempty"`
	Source   string           `json:"source,omitempty"`   // e.g. "Claude Code"
	Priority string           `json:"priority,omitempty"` // low | normal (default) | high
}

func (c *Client) Notify(ctx context.Context, r NotifyRequest, idemKey string) (*Notification, error) {
	var n Notification
	return &n, c.do(ctx, http.MethodPost, "/v1/notifications", idemKey, r, &n)
}

type AskRequest struct {
	Question string   `json:"question"`
	Body     string   `json:"body,omitempty"`
	Source   string   `json:"source,omitempty"`
	Priority string   `json:"priority,omitempty"` // default high
	Choices  []string `json:"choices,omitempty"`  // empty means a free-text reply
	Timeout  int      `json:"timeout,omitempty"`  // seconds until expiry; default 900
}

func (c *Client) Ask(ctx context.Context, r AskRequest, idemKey string) (*Question, error) {
	var q Question
	return &q, c.do(ctx, http.MethodPost, "/v1/questions", idemKey, r, &q)
}

func (c *Client) Question(ctx context.Context, id string) (*Question, error) {
	var q Question
	return &q, c.do(ctx, http.MethodGet, "/v1/questions/"+url.PathEscape(id), "", nil, &q)
}

// WaitOnce long-polls for up to timeout and returns the question's state,
// which is still pending if nothing happened in time.
func (c *Client) WaitOnce(ctx context.Context, id string, timeout time.Duration) (*Question, error) {
	var q Question
	path := "/v1/questions/" + url.PathEscape(id) + "/wait?timeout=" + strconv.Itoa(int(timeout.Seconds()))
	return &q, c.do(ctx, http.MethodGet, path, "", nil, &q)
}

// Wait blocks until the question is resolved or ctx ends. Network failures and
// server restarts are retried with backoff, so a waiting process survives them.
// onRetry, if non-nil, is called for each transient failure.
func (c *Client) Wait(ctx context.Context, id string, onRetry func(error)) (*Question, error) {
	backoff := time.Second
	for {
		q, err := c.WaitOnce(ctx, id, 55*time.Second)
		if err == nil {
			if !q.Pending() {
				return q, nil
			}
			backoff = time.Second
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ae *Error
		if errors.Is(err, ErrNoServer) || (errors.As(err, &ae) && !ae.Temporary()) {
			return nil, err
		}
		if onRetry != nil {
			onRetry(err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (c *Client) Cancel(ctx context.Context, id string) (*Question, error) {
	var q Question
	return &q, c.do(ctx, http.MethodPost, "/v1/questions/"+url.PathEscape(id)+"/cancel", "", nil, &q)
}

type WhoAmI struct {
	PushRegistered *bool  `json:"push_registered"` // nil on older API versions
	Key            Key    `json:"key"`
	Device         Device `json:"device"`
}

// HasPushRegistration reports registration for the API's current transport.
func (w WhoAmI) HasPushRegistration() bool {
	if w.PushRegistered != nil {
		return *w.PushRegistered
	}
	return w.Device.HasAPNsToken
}

func (c *Client) WhoAmI(ctx context.Context) (*WhoAmI, error) {
	var w WhoAmI
	return &w, c.do(ctx, http.MethodGet, "/v1/whoami", "", nil, &w)
}

// ---- mobile API (device token) ----

type Registration struct {
	Device      Device `json:"device"`
	DeviceToken string `json:"device_token"`
	Key         Key    `json:"key"`
}

// Register creates an anonymous device. It needs no token.
func (c *Client) Register(ctx context.Context, name string) (*Registration, error) {
	var r Registration
	return &r, c.do(ctx, http.MethodPost, "/v1/devices", "", map[string]string{"name": name}, &r)
}

// Respond answers a question; idemKey is required and must be reused on retries.
func (c *Client) Respond(ctx context.Context, questionID, answer, idemKey string) (*Question, error) {
	var q Question
	return &q, c.do(ctx, http.MethodPost, "/v1/questions/"+url.PathEscape(questionID)+"/responses", idemKey,
		map[string]string{"answer": answer}, &q)
}

func (c *Client) Inbox(ctx context.Context, cursor string, limit int) ([]Notification, string, error) {
	var out struct {
		Notifications []Notification `json:"notifications"`
		NextCursor    *string        `json:"next_cursor"`
	}
	q := url.Values{}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	err := c.do(ctx, http.MethodGet, "/v1/inbox?"+q.Encode(), "", nil, &out)
	next := ""
	if out.NextCursor != nil {
		next = *out.NextCursor
	}
	return out.Notifications, next, err
}

func (c *Client) CreateKey(ctx context.Context, name string, scopes []string) (*Key, error) {
	var k Key
	return &k, c.do(ctx, http.MethodPost, "/v1/keys", "", map[string]any{"name": name, "scopes": scopes}, &k)
}

func (c *Client) RevokeKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/keys/"+url.PathEscape(id), "", nil, nil)
}
