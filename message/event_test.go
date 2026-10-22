package message

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestExplicitEventContract(t *testing.T) {
	for _, event := range []string{"result", "attention", "failure"} {
		n, err := Decode([]byte(`{"title":"自定义标题","event":"` + event + `","type":"info","priority":"low"}`))
		if err != nil {
			t.Fatal(err)
		}
		for name, raw := range map[string][]byte{"request": mustEventJSON(t, n), "canonical": n.CanonicalJSON()} {
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			if object["event"] != event || object["title"] != "自定义标题" || object["type"] != "info" || object["priority"] != "low" {
				t.Fatalf("%s lost or coupled event: %s", name, raw)
			}
		}
	}
	for _, bad := range []string{"success", "error", "RESULT", " result", "result ", strings.Repeat("x", 1000)} {
		if _, err := Decode([]byte(`{"title":"test","event":"` + bad + `"}`)); err == nil {
			t.Fatalf("accepted event %q", bad)
		}
	}
	for _, title := range []string{"New result", "Run failed", "需要你确认"} {
		n, err := Decode([]byte(`{"title":"` + title + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mustEventJSON(t, n)), `"event"`) {
			t.Fatal("inferred event from title")
		}
	}
}

func mustEventJSON(t *testing.T, n Notification) []byte {
	t.Helper()
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestUnspecifiedEventPreservesCanonicalBytes(t *testing.T) {
	const previous = `{"type":"info","title":"old","body":"","source":"Codex","priority":"normal"}`
	for _, raw := range []string{`{"title":"old","source":"Codex"}`, `{"title":"old","source":"Codex","event":""}`} {
		n, err := Decode([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if string(n.CanonicalJSON()) != previous {
			t.Fatalf("old fingerprint changed: %s", n.CanonicalJSON())
		}
	}
}

// Generated cross-product checks normalization and serialization independently
// of the host's language, title and interruption policy.
func TestEventRoundTripProperties(t *testing.T) {
	for _, event := range []EventKind{"", EventResult, EventAttention, EventFailure} {
		for _, kind := range []string{TypeInfo, TypeAlert} {
			for _, priority := range []string{PriorityLow, PriorityNormal, PriorityHigh} {
				for i := 0; i < 64; i++ {
					original := Notification{Event: event, Type: kind, Priority: priority, Title: "  部署 😺 " + strings.Repeat("é", i) + "  ", Body: " **原文** ", Context: &Context{Session: " 审查 "}}
					normalized, err := original.Normalize()
					if err != nil {
						t.Fatal(err)
					}
					again, err := normalized.Normalize()
					if err != nil || !reflect.DeepEqual(normalized, again) {
						t.Fatal("not idempotent")
					}
					round, err := Decode(normalized.CanonicalJSON())
					if err != nil || !reflect.DeepEqual(round, normalized) {
						t.Fatalf("roundtrip=%v %v", round, err)
					}
					if normalized.Event != event || normalized.Type != kind || normalized.Priority != priority || original.Context.Session != " 审查 " {
						t.Fatal("event coupled or mutated caller")
					}
				}
			}
		}
	}
}

func TestEventSchemaEnum(t *testing.T) {
	b, err := os.ReadFile("../contracts/notification-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	got := defs["event"].(map[string]any)["enum"]
	if !reflect.DeepEqual(got, []any{string(EventResult), string(EventAttention), string(EventFailure)}) {
		t.Fatalf("enum drift=%v", got)
	}
	for _, name := range []string{"request", "record"} {
		props := defs[name].(map[string]any)["properties"].(map[string]any)
		if props["event"].(map[string]any)["$ref"] != "#/$defs/event" {
			t.Fatal("event schema not shared")
		}
	}
}
