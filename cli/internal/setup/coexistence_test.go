package setup

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCoexistenceOwnershipAndDecoding(t *testing.T) {
	for _, value := range []string{`not-json`, `[]`, `null`, `[""]`, `[1]`} {
		if _, err := DecodeForwardNotify(value); err == nil {
			t.Fatalf("invalid command accepted: %s", value)
		}
	}
	original := []string{"/opt/Computer Use/helper", "turn-ended"}
	encoded, _ := json.Marshal(original)
	valid := []string{"/custom/phone-push", "hook", "codex", "--no-summary", ForwardNotifyPrefix + string(encoded)}
	source := []byte("notify = " + TOMLArray(valid) + "\n")
	if _, ours := CodexNotify(source, "/custom/phone-push"); !ours {
		t.Fatal("custom wrapper not recognized")
	}
	if restored, err := CodexOriginalNotify(source, "/custom/phone-push"); err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("decode: %v %v", restored, err)
	}
	for _, argv := range [][]string{
		{"meow", "hook", "codex", "--no-summary", "--no-summary"},
		{"meow", "hook", "codex", ForwardNotifyPrefix + "bad"},
		{"meow", "hook", "codex", "--unknown"},
		{"meow", "hook", "codex", ForwardNotifyPrefix + string(encoded), ForwardNotifyPrefix + string(encoded)},
		{"meow", "hook", "codex", "a", "b", "c"},
	} {
		if _, ours := CodexNotify([]byte("notify = " + TOMLArray(argv) + "\n")); ours {
			t.Fatalf("unsafe ownership: %q", argv)
		}
	}
	for _, bad := range []string{"notify = [", "notify = 1", "notify = [\"\"]"} {
		if _, err := CodexOriginalNotify([]byte(bad)); err == nil {
			t.Fatalf("bad config accepted: %s", bad)
		}
	}
	if original, err := CodexOriginalNotify([]byte("notify = []\n")); err != nil || len(original) != 0 {
		t.Fatal(original, err)
	}
	if _, ours := CodexNotify([]byte("notify = 1")); ours {
		t.Fatal("invalid notify type became ours")
	}
	if !strings.Contains(string(source), "--forward-notify=") {
		t.Fatal("fixture missing wrapper")
	}
}
