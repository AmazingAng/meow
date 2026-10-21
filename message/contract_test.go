package message

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestProtocolFixtures(t *testing.T) {
	data, err := os.ReadFile("../contracts/fixtures/notifications.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Requests []struct {
			Name              string
			Input, Normalized json.RawMessage
		}
		Invalid []struct {
			Name, Field string
			Input       json.RawMessage
		}
		Records []struct {
			Name         string
			Notification json.RawMessage
		}
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Requests) == 0 || len(f.Invalid) == 0 || len(f.Records) == 0 {
		t.Fatal("empty fixtures")
	}
	for _, c := range f.Requests {
		t.Run(c.Name, func(t *testing.T) {
			got, err := Decode(c.Input)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(got)
			var a, w map[string]any
			_ = json.Unmarshal(b, &a)
			_ = json.Unmarshal(c.Normalized, &w)
			if !reflect.DeepEqual(a, w) {
				t.Fatalf("normalized=%s want=%s", b, c.Normalized)
			}
		})
	}
	for _, c := range f.Invalid {
		t.Run(c.Name, func(t *testing.T) {
			_, err := Decode(c.Input)
			var v *ValidationError
			if !errors.As(err, &v) || v.Field != c.Field {
				t.Fatalf("validation=%v", err)
			}
		})
	}
	for _, c := range f.Records {
		t.Run(c.Name, func(t *testing.T) {
			var n Record
			if err := json.Unmarshal(c.Notification, &n); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(n)
			var a, w map[string]any
			_ = json.Unmarshal(b, &a)
			_ = json.Unmarshal(c.Notification, &w)
			if !reflect.DeepEqual(a, w) {
				t.Fatalf("record drift=%s", b)
			}
		})
	}
}

func TestNormalizationIsIndependentAndIdempotent(t *testing.T) {
	for i := 0; i < 128; i++ {
		source := fmt.Sprintf("服务 %d", i)
		n := Notification{Title: " 完成 😺 ", Body: strings.Repeat("猫", i), Source: source, Context: &Context{Workspace: " 工程 ", Session: " 审查 ", SessionID: fmt.Sprint(i)}}
		first, err := n.Normalize()
		if err != nil {
			t.Fatal(err)
		}
		again, err := first.Normalize()
		if err != nil || !reflect.DeepEqual(first, again) {
			t.Fatal("normalization is not idempotent")
		}
		if n.Context.Workspace != " 工程 " || n.Context.Session != " 审查 " || first.Context == n.Context {
			t.Fatal("caller mutated")
		}
		decoded, err := Decode(first.CanonicalJSON())
		if err != nil || !reflect.DeepEqual(decoded, first) {
			t.Fatalf("round trip=%v %v", decoded, err)
		}
	}
	n, err := (Notification{Title: "legacy-v2", Source: "Codex"}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if string(n.CanonicalJSON()) != `{"type":"info","title":"legacy-v2","body":"","source":"Codex","priority":"normal"}` {
		t.Fatalf("legacy canonical=%s", n.CanonicalJSON())
	}
}

func TestLimitsAndValidation(t *testing.T) {
	cases := []struct {
		n     Notification
		field string
	}{
		{Notification{Title: strings.Repeat("猫", MaxTitle+1)}, "title"},
		{Notification{Title: "x", Body: strings.Repeat("猫", MaxBody+1)}, "body"},
		{Notification{Title: "x", Source: strings.Repeat("猫", MaxSource+1)}, "source"},
		{Notification{Title: "x", Body: "\x00"}, "body"},
		{Notification{Title: string([]byte{0xff})}, "title"},
	}
	for _, c := range cases {
		_, err := c.n.Normalize()
		var v *ValidationError
		if !errors.As(err, &v) || v.Field != c.field {
			t.Fatalf("got %v want %s", err, c.field)
		}
	}
	for _, field := range []string{"workspace", "workspace_id", "session", "session_id"} {
		max := MaxContextName
		if strings.HasSuffix(field, "_id") {
			max = MaxContextID
		}
		for _, value := range []string{strings.Repeat("猫", max+1), "a\nb", "a\x00b"} {
			b, _ := json.Marshal(map[string]any{"title": "x", "context": map[string]string{field: value}})
			if _, err := Decode(b); err == nil {
				t.Fatalf("invalid %s accepted", field)
			}
		}
	}
	for _, data := range [][]byte{[]byte("null"), []byte("[]"), []byte("bad"), bytes.Repeat([]byte{' '}, MaxJSONBytes+1)} {
		if _, err := Decode(data); err == nil {
			t.Fatal("invalid JSON accepted")
		}
	}
	for _, key := range []string{"bad key", "a\nb", strings.Repeat("x", 129), "中文"} {
		if ValidateIdempotencyKey(key) == nil {
			t.Fatal("invalid header accepted")
		}
	}
	for _, key := range []string{"", "ci-run-1", strings.Repeat("x", 128)} {
		if err := ValidateIdempotencyKey(key); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPreviewContractAndUtilities(t *testing.T) {
	b, err := os.ReadFile("../ios/MeowTests/Fixtures/notification-preview.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct{ Name, Body, Expected string }
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if got := Preview(f.Body); got != f.Expected {
			t.Fatalf("%s preview=%q", f.Name, got)
		}
	}
	for i := 0; i < 512; i++ {
		s := strings.Repeat("猫😺\n", i)
		got := Preview(s)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > MaxPreviewRunes || strings.Count(got, "\n") >= MaxPreviewLines {
			t.Fatal("preview bounds")
		}
	}
	if Preview("ordinary") != "ordinary" || Truncate("abc", 0) != "" || Truncate("abc", 1) != "…" || CleanLabel(" a\x00b\n ", 20) != "a b" {
		t.Fatal("utility semantics")
	}
}

func TestSchemaTracksProtocol(t *testing.T) {
	b, err := os.ReadFile("../contracts/notification-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	for name, model := range map[string]any{"request": Notification{}, "record": Record{}, "context": Context{}} {
		props := defs[name].(map[string]any)["properties"].(map[string]any)
		typ := reflect.TypeOf(model)
		if len(props) != typ.NumField() {
			t.Fatalf("%s field count drift", name)
		}
		for i := 0; i < typ.NumField(); i++ {
			key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if props[key] == nil {
				t.Fatalf("schema missing %s.%s", name, key)
			}
		}
	}
	req := defs["request"].(map[string]any)
	p := req["properties"].(map[string]any)
	for field, limit := range map[string]int{"title": MaxTitle, "body": MaxBody, "source": MaxSource} {
		if p[field].(map[string]any)["maxLength"] != float64(limit) {
			t.Fatal("schema limit drift")
		}
	}
	if !reflect.DeepEqual(p["type"].(map[string]any)["enum"], []any{TypeInfo, TypeAlert}) ||
		!reflect.DeepEqual(p["priority"].(map[string]any)["enum"], []any{PriorityLow, PriorityNormal, PriorityHigh}) {
		t.Fatal("schema enums drift")
	}
	cprops := defs["context"].(map[string]any)["properties"].(map[string]any)
	for field, limit := range map[string]int{"workspace": MaxContextName, "session": MaxContextName, "workspace_id": MaxContextID, "session_id": MaxContextID} {
		if cprops[field].(map[string]any)["maxLength"] != float64(limit) {
			t.Fatal("schema context limit drift")
		}
	}
	preview := schema["x-meow-banner"].(map[string]any)
	if preview["maxRunes"] != float64(MaxPreviewRunes) || preview["maxLines"] != float64(MaxPreviewLines) {
		t.Fatal("schema preview limit drift")
	}
	if !reflect.DeepEqual(req["required"], []any{"title"}) || p["type"].(map[string]any)["default"] != TypeInfo || p["priority"].(map[string]any)["default"] != PriorityNormal {
		t.Fatal("schema defaults drift")
	}
}

func TestFailedNormalizationDoesNotAliasCallerContext(t *testing.T) {
	n := Notification{Context: &Context{Session: "Original"}}
	bad, err := n.Normalize()
	if err == nil {
		t.Fatal("missing title accepted")
	}
	bad.Context.Session = "Changed"
	if n.Context.Session != "Original" {
		t.Fatal("error result aliases caller-owned context")
	}
}

func TestValidationErrorsDoNotIncludePrivateValues(t *testing.T) {
	_, err := (Notification{Title: "x", Source: strings.Repeat("PRIVATE", 20)}).Normalize()
	if err == nil || err.Error() != "source must be at most 64 characters" {
		t.Fatalf("validation error=%v", err)
	}
}
