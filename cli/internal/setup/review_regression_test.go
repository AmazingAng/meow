package setup

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/pelletier/go-toml/v2"
)

func decodeTOML(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := toml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("invalid TOML: %v\n%s", err, b)
	}
	return doc
}

func TestCodexQuotedNotifyConflict(t *testing.T) {
	for _, key := range []string{`"notify"`, `'notify'`, `"\u006eotify"`} {
		t.Run(key, func(t *testing.T) {
			src := []byte(key + " = [\"python3\", \"notify.py\"]\nmodel = \"o3\"\n")
			decodeTOML(t, src)
			existing, ours := CodexNotify(src)
			if existing == "" || ours {
				t.Errorf("existing=%q ours=%v", existing, ours)
			}
			_, err := CodexConfig(src, codexArgv, true, false)
			var conflict *NotifyConflictError
			if !errors.As(err, &conflict) {
				t.Errorf("expected conflict, got %v", err)
			}
			removed, err := CodexConfig(src, nil, false, false)
			if err != nil || !bytes.Equal(removed, src) {
				t.Fatalf("foreign notify changed: %s %v", removed, err)
			}
			forced, err := CodexConfig(src, codexArgv, true, true)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeTOML(t, forced)
			if !reflect.DeepEqual(got["notify"], []any{codexArgv[0], "hook", "codex"}) || got["model"] != "o3" {
				t.Fatal(got)
			}
		})
	}
}

func TestCodexParserKeepsNotifyAtRoot(t *testing.T) {
	cases := []string{
		"# use \"\"\" for multiline text\nmodel = \"o3\"\n\n[tui]\nnotifications = true\n",
		"# use ''' for multiline text\nmodel = \"o3\"\n\n[tui]\nnotifications = true\n",
		"developer_instructions = '''\n[not a table]\nnotify = [\"not\", \"a\", \"command\"]\n'''\n\n[tui]\nnotifications = false\n",
		"developer_instructions = \"\"\"\nUse an escaped quote \\\" and an escaped triple \\\"\"\" inside instructions.\n\"\"\"\n[tui]\nnotifications = true\n",
		"model = \"o3\"\r\n\r\n[[projects]]\r\nname = \"test\"\r\n",
		"model = \"o3\"\n",
		"model = \"o3\"",
	}
	for i, src := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			before := decodeTOML(t, []byte(src))
			installed, err := CodexConfig([]byte(src), codexArgv, true, false)
			if err != nil {
				t.Fatal(err)
			}
			after := decodeTOML(t, installed)
			if after["notify"] == nil {
				t.Fatalf("notify is not at root:\n%s", installed)
			}
			delete(after, "notify")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("unrelated values changed: %#v -> %#v", before, after)
			}
			twice, err := CodexConfig(installed, codexArgv, true, false)
			if err != nil || !bytes.Equal(installed, twice) {
				t.Fatalf("not idempotent: %s %v", twice, err)
			}
			removed, err := CodexConfig(installed, nil, false, false)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, decodeTOML(t, removed)) {
				t.Fatalf("uninstall changed unrelated values: %s", removed)
			}
			if strings.HasSuffix(src, "\n") && !bytes.Equal([]byte(src), removed) {
				t.Fatalf("unrelated bytes changed:\n%q\n%q", src, removed)
			}
		})
	}
}

func TestCodexRejectsInvalidDocuments(t *testing.T) {
	for _, src := range []string{"notify = [", "notify = []\nnotify = []\n", "[tui\n", "a = 1\na.b = 2\n"} {
		if out, err := CodexConfig([]byte(src), codexArgv, true, false); err == nil || out != nil {
			t.Errorf("invalid input accepted: %q -> %q %v", src, out, err)
		}
		if existing, ours := CodexNotify([]byte(src)); existing != "" || ours {
			t.Errorf("invalid input identified as configured: %q %v", existing, ours)
		}
	}
}

func TestCodexForeignHookArgumentsArePreserved(t *testing.T) {
	for _, argv := range [][]string{
		{"/opt/another-agent", "hook", "codex"},
		{"/opt/meow", "other", "hook", "codex"},
		{"/opt/meow", "hook", "codex", "--another-operation"},
		{"/opt/meow", "hook", "codex", "--no-summary", "extra"},
	} {
		src := []byte("notify = " + TOMLArray(argv) + "\n")
		if _, ours := CodexNotify(src); ours {
			t.Errorf("claimed foreign command %v", argv)
		}
		if _, err := CodexConfig(src, codexArgv, true, false); err == nil {
			t.Errorf("overwrote foreign command %v", argv)
		}
		out, err := CodexConfig(src, nil, false, false)
		if err != nil || !bytes.Equal(src, out) {
			t.Errorf("removed foreign command %v: %q %v", argv, out, err)
		}
	}
	for _, src := range []string{`notify = ["meow", "hook", "codex"]`, `notify = ['C:\tools\meow.exe', 'hook', 'codex', '--no-summary']`} {
		if _, ours := CodexNotify([]byte(src)); !ours {
			t.Errorf("lost legitimate Meow command %s", src)
		}
	}
}

func TestCodexNotifyOwnershipRequiresStringArguments(t *testing.T) {
	for _, src := range []string{"notify = 42", `notify = ["meow", "hook", 3]`, `notify = ["meow", "hook", "codex"`} {
		if isMeowNotify(src) {
			t.Errorf("invalid command claimed as Meow: %s", src)
		}
	}
}

func TestCodexGeneratedRoundTrips(t *testing.T) {
	property := func(raw string, quoted bool, crlf bool) bool {
		raw = strings.ToValidUTF8(raw, "?")
		key := "notify"
		if quoted {
			key = `"notify"`
		}
		src := []byte("developer_instructions = " + tomlString(raw) + "\n# \"\"\" ''' [header]\n" + key + " = [\"meow\", \"hook\", \"codex\"]\n\n[tui]\nnotifications = true\n")
		if crlf {
			src = bytes.ReplaceAll(src, []byte("\n"), []byte("\r\n"))
		}
		var before map[string]any
		if toml.Unmarshal(src, &before) != nil {
			return false
		}
		out, err := CodexConfig(src, codexArgv, true, false)
		if err != nil {
			return false
		}
		var after map[string]any
		if toml.Unmarshal(out, &after) != nil {
			return false
		}
		delete(before, "notify")
		delete(after, "notify")
		if !reflect.DeepEqual(before, after) {
			return false
		}
		twice, err := CodexConfig(out, codexArgv, true, false)
		if err != nil || !bytes.Equal(twice, out) {
			return false
		}
		removed, err := CodexConfig(out, nil, false, false)
		if err != nil || toml.Unmarshal(removed, &after) != nil {
			return false
		}
		delete(after, "notify")
		return reflect.DeepEqual(before, after)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 200, Rand: rand.New(rand.NewSource(20261018))}); err != nil {
		t.Fatal(err)
	}
}

func TestCodexCustomExecutableOwnership(t *testing.T) {
	argv := []string{"/private/tools/phone-push", "hook", "codex"}
	once, err := CodexConfig(nil, argv, true, false)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := CodexConfig(once, argv, true, false)
	if err != nil || !bytes.Equal(once, twice) {
		t.Fatalf("custom executable reinstall failed: %s %v", twice, err)
	}
}

func FuzzCodexConfigPreservesSettings(f *testing.F) {
	for _, src := range []string{
		"", "# \"\"\"\n[tui]\nnotifications = true\n",
		"\"notify\" = [\"meow\", \"hook\", \"codex\"]\n[tui]\nnotifications = false\n",
		"developer_instructions = '''\n[not a table]\n'''\n[tui]\nnotifications = true\n",
		"[projects.\"/tmp/work\"]\ntrust_level = \"trusted\"\n",
		"notify = [\n\"/opt/foreign\",\n\"hook\",\n\"codex\",\n]\n",
		"notify = [", "x = 1\nx.y = 2\n",
	} {
		f.Add([]byte(src))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		var before map[string]any
		invalid := toml.Unmarshal(src, &before)
		// Codex's notify schema is an array of strings. Other shapes are not a
		// working Codex configuration and are outside this preservation property.
		if value, ok := before["notify"]; invalid == nil && ok {
			values, array := value.([]any)
			if !array {
				t.Skip()
			}
			for _, item := range values {
				if _, ok := item.(string); !ok {
					t.Skip()
				}
			}
		}
		out, err := CodexConfig(src, codexArgv, true, true)
		if invalid != nil {
			if err == nil || out != nil {
				t.Fatal("invalid TOML was accepted")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		after := decodeTOML(t, out)
		if !reflect.DeepEqual(after["notify"], []any{codexArgv[0], "hook", "codex"}) {
			t.Fatal("root notify missing")
		}
		delete(before, "notify")
		delete(after, "notify")
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("unrelated settings changed: %#v -> %#v", before, after)
		}
		twice, err := CodexConfig(out, codexArgv, true, false)
		if err != nil || !bytes.Equal(twice, out) {
			t.Fatalf("reinstall changed output: %s %v", twice, err)
		}
	})
}
