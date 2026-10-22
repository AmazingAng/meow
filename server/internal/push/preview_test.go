package push

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"meow.blue/meow/server/internal/store"
)

func TestNotificationPreviewContract(t *testing.T) {
	b, err := os.ReadFile("../../../ios/MeowTests/Fixtures/notification-preview.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Name, Body, Expected string }
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			m, err := BuildMessage(store.PushTarget{Notification: store.Notification{Title: "Result", Body: tc.Body, Source: "Codex"}})
			if err != nil {
				t.Fatal(err)
			}
			var p struct {
				APS  struct{ Alert struct{ Title, Body string } }
				Meow struct {
					PreviewFormat string `json:"preview_format"`
				}
			}
			json.Unmarshal(m.Payload, &p)
			if p.Meow.PreviewFormat != "plain" {
				t.Fatal("plain preview must not be parsed twice by iOS")
			}
			expected := "Result"
			if tc.Expected != "" {
				expected += "\n" + tc.Expected
			}
			expected = truncate(expected, 180)
			if p.APS.Alert.Body != expected {
				t.Fatalf("preview=%q want=%q", p.APS.Alert.Body, expected)
			}
		})
	}
}

func FuzzPreviewBounded(f *testing.F) {
	for _, s := range []string{"中文\nHello", "{\"x\":1}", "😺", strings.Repeat("x", 200)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			return
		}
		got := notificationPreview(s)
		if !utf8.ValidString(got) || utf8.RuneCountInString(got) > 180 || strings.Count(got, "\n") > 2 {
			t.Fatalf("invalid preview %q", got)
		}
		if s == "Hello" && got != s {
			t.Fatal("lost ordinary text")
		}
	})
}

// The source must be rendered before either the body preview or the final
// title+body payload is shortened. iOS cannot recover discarded delimiters.
func TestBuildMessageParsesLongMarkdownBeforeTruncating(t *testing.T) {
	for _, source := range []string{"", "Codex"} {
		n := store.Notification{Title: "New result", Body: "**" + strings.Repeat("猫", 200) + "**", Source: source}
		m, err := BuildMessage(store.PushTarget{Notification: n})
		if err != nil {
			t.Fatal(err)
		}
		var payload struct {
			APS struct{ Alert struct{ Body string } }
		}
		if err := json.Unmarshal(m.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		prefix := ""
		if source != "" {
			prefix = "New result\n"
		}
		want := truncate(prefix+strings.Repeat("猫", 200), 180)
		if payload.APS.Alert.Body != want {
			t.Fatalf("source=%q got=%q want=%q", source, payload.APS.Alert.Body, want)
		}
	}
}
