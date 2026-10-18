// Package push delivers notifications to iOS devices through APNs.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	ProductionURL = "https://api.push.apple.com"
	SandboxURL    = "https://api.sandbox.push.apple.com"

	// Apple rejects provider tokens older than an hour and throttles refreshes
	// more frequent than every 20 minutes.
	tokenTTL = 50 * time.Minute
)

// Message is one APNs request.
type Message struct {
	Token      string // hex device token
	Sandbox    bool
	Priority   string // low | normal | high
	CollapseID string
	Expiration time.Time
	Payload    []byte
}

type Pusher interface {
	Push(ctx context.Context, m Message) error
}

// ErrNotConfigured is returned by Disabled; such notifications are recorded as skipped.
var ErrNotConfigured = errors.New("APNs is not configured")

// Error is a non-200 APNs response.
type Error struct {
	Status int
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("apns: %d %s", e.Status, e.Reason) }

// InvalidToken reports whether the device token should be discarded.
func (e *Error) InvalidToken() bool {
	return e.Status == http.StatusGone || e.Reason == "BadDeviceToken" || e.Reason == "Unregistered" || e.Reason == "DeviceTokenNotForTopic"
}

func (e *Error) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// APNs sends pushes with token-based (.p8) authentication over HTTP/2.
type APNs struct {
	KeyID         string
	TeamID        string
	Topic         string // the iOS app bundle identifier
	ProductionURL string
	SandboxURL    string
	HTTP          *http.Client
	Now           func() time.Time

	key   *ecdsa.PrivateKey
	mu    sync.Mutex
	jwt   string
	jwtAt time.Time
}

func NewAPNs(p8 []byte, keyID, teamID, topic string) (*APNs, error) {
	if keyID == "" || teamID == "" || topic == "" {
		return nil, errors.New("apns: key ID, team ID and topic are all required")
	}
	block, _ := pem.Decode(p8)
	if block == nil {
		return nil, errors.New("apns: auth key is not PEM encoded")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns: parse auth key: %w", err)
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns: auth key is not an ES256 key")
	}
	return &APNs{
		KeyID: keyID, TeamID: teamID, Topic: topic,
		ProductionURL: ProductionURL, SandboxURL: SandboxURL,
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{ForceAttemptHTTP2: true, MaxIdleConnsPerHost: 4, IdleConnTimeout: 5 * time.Minute},
		},
		Now: time.Now,
		key: ek,
	}, nil
}

// token returns a cached provider JWT, minting a new one when it is old or equals stale.
func (a *APNs) token(stale string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.Now()
	if a.jwt != "" && a.jwt != stale && now.Sub(a.jwtAt) < tokenTTL {
		return a.jwt, nil
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": now.Unix()})
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.jwt, a.jwtAt = input+"."+base64.RawURLEncoding.EncodeToString(sig), now
	return a.jwt, nil
}

func (a *APNs) Push(ctx context.Context, m Message) error {
	tok, err := a.token("")
	if err != nil {
		return err
	}
	err = a.send(ctx, m, tok)
	var ae *Error
	if errors.As(err, &ae) && ae.Reason == "ExpiredProviderToken" {
		if tok, err = a.token(tok); err != nil {
			return err
		}
		err = a.send(ctx, m, tok)
	}
	return err
}

func (a *APNs) send(ctx context.Context, m Message, tok string) error {
	base := a.ProductionURL
	if m.Sandbox {
		base = a.SandboxURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+m.Token, bytes.NewReader(m.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	priority := "10"
	if m.Priority == "low" {
		priority = "5"
	}
	req.Header.Set("apns-priority", priority)
	if !m.Expiration.IsZero() {
		req.Header.Set("apns-expiration", strconv.FormatInt(m.Expiration.Unix(), 10))
	}
	if m.CollapseID != "" {
		req.Header.Set("apns-collapse-id", m.CollapseID)
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	return &Error{Status: resp.StatusCode, Reason: body.Reason}
}

// Disabled is used when no APNs credentials are configured, e.g. in local development.
type Disabled struct{ Log *slog.Logger }

func (d Disabled) Push(_ context.Context, m Message) error {
	d.Log.Debug("push skipped: APNs not configured", "payload", string(m.Payload))
	return ErrNotConfigured
}
