package adapters

import (
	"bytes"
	"encoding/json"
	"meow.blue/meow/message"
	"os"
	"testing"
)

func TestFrozenAgentNotifications(t *testing.T) {
	data, err := os.ReadFile("../contracts/fixtures/agent-notifications.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Kind     string
		Input          string
		Summary        bool
		Language       string
		Notification   json.RawMessage
		IdempotencyKey string
		Send, Error    bool
	}
	if err = json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 11 {
		t.Fatal("empty/incomplete compatibility fixtures")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			r, err := Build(f.Kind, []byte(f.Input), Options{IncludeSummary: f.Summary, Language: f.Language})
			if (err != nil) != f.Error || r.Send != f.Send || r.IdempotencyKey != f.IdempotencyKey {
				t.Fatalf("result=%+v err=%v expected key=%s", r, err, f.IdempotencyKey)
			}
			got, _ := json.Marshal(r.Notification)
			var expected bytes.Buffer
			if err = json.Compact(&expected, f.Notification); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, expected.Bytes()) {
				t.Fatalf("notification=%s want=%s", got, expected.Bytes())
			}
		})
	}
}

type serviceAdapter struct{}

func (serviceAdapter) Decode([]byte) (Event, bool, error) {
	return Event{Notification: message.Notification{Title: "Deploy completed", Source: "CI", Body: "full body"}}, true, nil
}

func TestCustomAndGenericAdapters(t *testing.T) {
	r, err := Adapt(serviceAdapter{}, []byte(`{"result":"ok"}`), Options{})
	if err != nil || !r.Send || r.Notification.Source != "CI" || r.Notification.Body != "full body" {
		t.Fatalf("custom=%+v %v", r, err)
	}
	r, err = Build("generic", []byte(`{"title":" Build ","body":" **Done** ","source":" CI ","context":{"session_id":"run-1"}}`), Options{})
	if err != nil || r.Notification.Title != "Build" || r.Notification.Body != "**Done**" || r.Notification.Type != "info" || r.Notification.Priority != "normal" || r.IdempotencyKey != "" {
		t.Fatalf("service=%+v %v", r, err)
	}
	for _, name := range []string{"unknown", ""} {
		if _, err = Build(name, []byte(`{"title":"x"}`), Options{}); err == nil {
			t.Fatal("unknown accepted")
		}
	}
	if _, err = Adapt(nil, []byte(`{"title":"x"}`), Options{}); err == nil {
		t.Fatal("nil adapter")
	}
}

func TestInvalidAdapterInputsAndResolver(t *testing.T) {
	for _, name := range []string{"generic", "claude-stop", "codex", "codex-stop"} {
		for _, input := range []string{"null", "[]", "{}", "bad", `{"title":`, string(bytes.Repeat([]byte{'x'}, MaxInputBytes+1))} {
			if _, err := Build(name, []byte(input), Options{}); err == nil {
				t.Fatalf("%s accepted invalid input", name)
			}
		}
	}
	_, err := Build("claude-stop", []byte(`{"message":"m","session_id":"s1","prompt_id":"p1"}`), Options{ResolveContext: func(o Origin) *message.Context {
		if o.Source != "Claude Code" || o.SessionID != "s1" {
			t.Fatalf("origin=%v", o)
		}
		return &message.Context{Session: "Resolved", SessionID: o.SessionID}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Build("generic", []byte(`{"title":"x","type":"question"}`), Options{}); err == nil {
		t.Fatal("invalid standard type")
	}
}

func TestFormattingAndPermissionBody(t *testing.T) {
	for _, c := range []struct{ kind, lang, want string }{{ResultTitle, "en", "New result"}, {ResultTitle, "zh", "有新结果"}, {AttentionTitle, "zh-Hans", "需要你确认"}, {FailureTitle, "zh-CN", "运行出错"}, {FailureTitle, "en", "Run failed"}, {AttentionTitle, "en", "Needs your attention"}} {
		if got := Title(c.kind, c.lang); got != c.want {
			t.Fatalf("title=%q", got)
		}
	}
	if WithProject("x", "") != "x" || WithProject("x", "/") != "x" || WithProject("x", ".") != "x" || WithProject("x", "/w/project") != "x · project" {
		t.Fatal("project presentation")
	}
	raw := []byte("{\"hook_event_name\":\"PermissionRequest\",\"session_id\":\"s\",\"turn_id\":\"t\",\"tool_name\":\"Bash\",\"tool_input\":{\n  \"command\": \"go test\"\n}}")
	r, err := Build("codex-permission", raw, Options{IncludeSummary: true})
	if err != nil || !r.Send || r.Notification.Body != "" {
		t.Fatalf("raw body=%q %v", r.Notification.Body, err)
	}
	if (Origin{}).Context() != nil {
		t.Fatal("empty context")
	}
	if c := (Origin{Cwd: "/", SessionName: "Named"}).Context(); c.Workspace != "" || c.Session != "Named" {
		t.Fatal(c)
	}
	if IdempotencyKey("a", "", "b") != "" || IdempotencyKey("a", "b") != IdempotencyKey("a", "b") {
		t.Fatal("key stability")
	}
}

func TestCustomOriginDoesNotRequireATitleTemplate(t *testing.T) {
	a := decoder(func([]byte) (Event, bool, error) {
		return Event{Notification: message.Notification{Title: "A service title", Source: "CI"}, Origin: Origin{SessionID: "run-1", SessionName: "Release"}}, true, nil
	})
	r, err := Adapt(a, []byte(`{"ok":true}`), Options{})
	if err != nil || r.Notification.Context == nil || r.Notification.Context.SessionID != "run-1" || r.Notification.Title != "A service title" {
		t.Fatalf("custom origin=%+v %v", r, err)
	}
	bad := decoder(func([]byte) (Event, bool, error) {
		return Event{Notification: message.Notification{Title: "x"}, Template: "unknown"}, true, nil
	})
	if _, err = Adapt(bad, []byte(`{"ok":true}`), Options{}); err == nil {
		t.Fatal("invalid template accepted")
	}
}

func TestSuggestionEnvelopesAndAdapterValidation(t *testing.T) {
	for _, c := range []struct {
		body   string
		silent bool
	}{
		{`{"suggestion":null}`, true}, {`{"suggestions":[]}`, true}, {`{"suggestions":[{"title":"Next"}]}`, true},
		{`{"exclude":[]}`, true}, {`{"exclude":[{"id":"candidate","reason":"Safety policy"}]}`, true},
		{`{"exclude":null}`, false}, {`{"exclude":[{"id":"candidate"}]}`, false},
		{`{"exclude":[1]}`, false}, {`{"exclude":[{"id":"candidate","reason":false}]}`, false},
		{`{"suggestions":[1]}`, false}, {`{"suggestions":[{"title":""}]}`, false}, {`{"suggestions":[{"title":1}]}`, false},
		{`{"other":1}`, false}, {`{"suggestion":1}`, false}, {`not JSON`, false},
	} {
		data, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s1", "turn_id": "t1", "transcript_path": nil, "last_assistant_message": c.body})
		r, err := Build("codex-stop", data, Options{IncludeSummary: true})
		if err != nil || r.Send == c.silent {
			t.Fatalf("body=%s result=%+v %v", c.body, r, err)
		}
	}
	if _, err := Build("claude-stop", []byte(`{"other":true}`), Options{}); err == nil {
		t.Fatal("missing Claude identity")
	}
	bad := decoder(func([]byte) (Event, bool, error) { return Event{Notification: message.Notification{}}, true, nil })
	if _, err := Adapt(bad, []byte(`{"ok":true}`), Options{}); err == nil {
		t.Fatal("invalid output accepted")
	}
}
