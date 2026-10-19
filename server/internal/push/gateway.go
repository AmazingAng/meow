package push

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"meow.blue/meow/server/internal/store"
)

// WakeRequest is intentionally incapable of carrying message content or URLs.
type WakeRequest struct {
	DeviceID       string    `json:"device_id"`
	NotificationID string    `json:"notification_id"`
	Priority       string    `json:"priority"`
	ExpiresAt      time.Time `json:"expires_at"`
}

var notificationIDRE = regexp.MustCompile(`^ntf_[a-zA-Z0-9_-]{1,64}$`)

func ValidNotificationID(id string) bool { return notificationIDRE.MatchString(id) }

func WakeMessage(r store.GatewayRoute, in WakeRequest) Message {
	aps := map[string]any{"alert": map[string]string{"title": "Meow", "body": "New message. Open Meow to view."}, "mutable-content": 1}
	if in.Priority != "low" {
		aps["sound"] = NotificationSound
	}
	aps["interruption-level"] = interruptionLevel(in.Priority)
	payload, _ := json.Marshal(map[string]any{"aps": aps, "meow": map[string]any{"id": in.NotificationID, "binding_id": r.BindingID, "relay": true}})
	return Message{Token: r.APNsToken, Sandbox: r.APNsEnv == "sandbox", Priority: in.Priority, Expiration: in.ExpiresAt, Payload: payload}
}

// GatewayURL accepts self-hosted HTTP/HTTPS endpoints without URL credentials,
// query or fragment. A reverse-proxy path prefix is supported.
func GatewayURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid gateway URL")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("gateway URL must use HTTP or HTTPS")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

type GatewayClient struct {
	BaseURL string
	HTTP    *http.Client
}

func NewGatewayClient(raw string) (*GatewayClient, error) {
	base, err := GatewayURL(raw)
	if err != nil {
		return nil, err
	}
	return &GatewayClient{BaseURL: base, HTTP: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Push satisfies Pusher; relay messages are built by relayMessage, never from
// a serialized direct APNs payload. Success requires explicit APNs acceptance.
func (g *GatewayClient) Push(ctx context.Context, m Message) error {
	if m.Wake == nil || m.Binding == nil || m.Binding.GatewayURL != g.BaseURL {
		return errors.New("gateway binding is missing or belongs to another gateway")
	}
	b, err := json.Marshal(m.Wake)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", g.BaseURL+"/v1/gateway/push", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.Binding.PushToken)
	res, err := g.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var result struct {
		Accepted bool `json:"accepted"`
	}
	if res.StatusCode != 200 {
		return &Error{Status: res.StatusCode, Reason: "GatewayRejected", RetryAfter: retryAfter(res.Header.Get("Retry-After"), time.Now())}
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 4097))
	if err := decoder.Decode(&result); err != nil || !result.Accepted {
		return fmt.Errorf("gateway did not confirm APNs acceptance")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("invalid gateway acceptance response")
	}
	return nil
}

func relayMessage(t store.PushTarget, b store.GatewayBinding) Message {
	expiry := t.Notification.CreatedAt.Add(24 * time.Hour)
	if q := t.Notification.Question; q != nil {
		expiry = q.ExpiresAt
	}
	return Message{Binding: &b, Wake: &WakeRequest{DeviceID: b.DeviceID, NotificationID: t.Notification.ID, Priority: t.Notification.Priority, ExpiresAt: expiry}}
}

// retryAfter accepts HTTP delay-seconds or an HTTP-date. Invalid values fall
// back to the dispatcher's ordinary backoff; overflow must never wrap negative.
func retryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.ParseUint(raw, 10, 63); err == nil {
		if seconds > uint64((1<<63-1)/int64(time.Second)) {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if deadline, err := http.ParseTime(raw); err == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}
