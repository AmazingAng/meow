package message

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func FuzzNormalizeRoundTrip(f *testing.F) {
	for _, seed := range [][4]string{{" 完成 ", "**正文** 😺", "CI", "会话"}, {"x", "a\nb", "", ""}, {"", "", "", ""}, {"x", "\x00", "s", "name"}} {
		f.Add(seed[0], seed[1], seed[2], seed[3])
	}
	f.Fuzz(func(t *testing.T, title, body, source, name string) {
		context := &Context{Session: CleanLabel(name, MaxContextName), SessionID: "s1"}
		n := Notification{Title: title, Body: body, Source: source, Context: context}
		original := *context
		got, err := n.Normalize()
		valid := func(value string, limit int, required bool) bool {
			s := strings.TrimSpace(value)
			return utf8.ValidString(s) && !strings.ContainsRune(s, 0) && utf8.RuneCountInString(s) <= limit && (!required || s != "")
		}
		expected := valid(title, MaxTitle, true) && valid(body, MaxBody, false) && valid(source, MaxSource, false)
		if (err == nil) != expected {
			t.Fatalf("normalization acceptance mismatch: %v", err)
		}
		if *context != original {
			t.Fatal("normalizer mutated input")
		}
		if err != nil {
			return
		}
		if got.Type != TypeInfo || got.Priority != PriorityNormal {
			t.Fatal("default drift")
		}
		if got.Body != strings.TrimSpace(body) {
			t.Fatal("original content lost")
		}
		again, err := got.Normalize()
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatal("not idempotent")
		}
		decoded, err := Decode(got.CanonicalJSON())
		if err != nil || !reflect.DeepEqual(decoded, got) || !bytes.Equal(decoded.CanonicalJSON(), got.CanonicalJSON()) {
			t.Fatal("wire round-trip drift")
		}
	})
}
