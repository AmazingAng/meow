// Package adapters translates external events into the standard Meow protocol.
// Adapters have no credentials, network, environment or filesystem access.
package adapters

import (
	"encoding/json"
	"fmt"
	"meow.blue/meow/message"
	"strings"
)

const MaxInputBytes = 1 << 20
const (
	ResultTitle    = "result"
	AttentionTitle = "attention"
	FailureTitle   = "failure"
)

// Event separates an external event's meaning from host-specific metadata lookup
// and notification presentation. A blank Template preserves a service's title.
type Event struct {
	Notification    message.Notification
	Origin          Origin
	Template        string
	DedupeNamespace string
	EventID         string
}

type Adapter interface {
	Decode([]byte) (Event, bool, error)
}
type decoder func([]byte) (Event, bool, error)

func (d decoder) Decode(data []byte) (Event, bool, error) { return d(data) }

// Options makes host policies explicit; no adapter reads ambient process state.
// ResolveBody supplies text the event itself does not carry. It runs only when
// a summary was requested and the decoded body is blank.
type Options struct {
	IncludeSummary bool
	Language       string
	ResolveContext func(Origin) *message.Context
	ResolveBody    func(Origin) string
}

type Result struct {
	Notification   message.Notification
	IdempotencyKey string
	Send           bool
}

func Lookup(name string) (Adapter, error) {
	switch name {
	case "claude-stop", "claude-notify", "claude-failure":
		return decoder(func(b []byte) (Event, bool, error) { return decodeClaude(name, b) }), nil
	case "codex":
		return decoder(decodeCodexNotify), nil
	case "codex-stop", "codex-permission":
		return decoder(func(b []byte) (Event, bool, error) { return decodeCodexHook(name, b) }), nil
	case "cursor-stop":
		return decoder(decodeCursorStop), nil
	case "generic":
		return decoder(decodeService), nil
	default:
		return nil, fmt.Errorf("unknown adapter %q", name)
	}
}

func Build(name string, data []byte, options Options) (Result, error) {
	a, err := Lookup(name)
	if err != nil {
		return Result{}, err
	}
	return Adapt(a, data, options)
}

// Adapt allows additional Go integrations to implement Adapter without changing
// the CLI. Payloads and ignored events follow the same bounded object contract.
func Adapt(a Adapter, data []byte, options Options) (Result, error) {
	if a == nil {
		return Result{}, fmt.Errorf("adapter is required")
	}
	if len(data) > MaxInputBytes {
		return Result{}, fmt.Errorf("adapter input exceeds 1 MiB")
	}
	if text := strings.TrimSpace(string(data)); !strings.HasPrefix(text, "{") || text == "{}" {
		return Result{}, fmt.Errorf("invalid adapter input: expected event object")
	}
	e, send, err := a.Decode(data)
	if err != nil || !send {
		return Result{}, err
	}
	n := e.Notification
	if e.Template != "" && e.Template != ResultTitle && e.Template != AttentionTitle && e.Template != FailureTitle {
		return Result{}, fmt.Errorf("unknown notification title template %q", e.Template)
	}
	if n.Context == nil && (e.Template != "" || e.Origin != (Origin{})) {
		e.Origin.Source = n.Source
		if options.ResolveContext != nil {
			n.Context = options.ResolveContext(e.Origin)
		} else {
			n.Context = e.Origin.Context()
		}
	}
	if e.Template != "" {
		if strings.TrimSpace(n.Title) == "" {
			n.Title = Title(e.Template, options.Language)
		}
		n.Title = WithProject(n.Title, e.Origin.Cwd)
		if options.IncludeSummary {
			if strings.TrimSpace(n.Body) == "" && options.ResolveBody != nil {
				n.Body = options.ResolveBody(e.Origin)
			}
			n.Body = Summary(n.Body)
		} else {
			n.Body = ""
		}
	}
	if _, err := n.Normalize(); err != nil {
		return Result{}, err
	}
	key := ""
	if e.DedupeNamespace != "" {
		b, _ := json.Marshal(n)
		key = IdempotencyKey(e.DedupeNamespace, e.Origin.SessionID, e.EventID, string(b))
	}
	return Result{Notification: n, IdempotencyKey: key, Send: true}, nil
}
