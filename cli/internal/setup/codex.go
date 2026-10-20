package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// isMeowNotify recognizes Meow's executable and the exact arguments setup writes.
// An unrelated command mentioning "hook codex" must never be removed.
func isMeowNotify(assignment string, knownExecutables ...string) bool {
	var value struct {
		Notify []string `toml:"notify"`
	}
	if toml.Unmarshal([]byte(assignment), &value) != nil {
		return false
	}
	_, ok := meowNotify(value.Notify, knownExecutables...)
	return ok
}

const ForwardNotifyPrefix = "--forward-notify="

// DecodeForwardNotify decodes argv, never shell source.
func DecodeForwardNotify(value string) ([]string, error) {
	var argv []string
	if err := json.Unmarshal([]byte(value), &argv); err != nil {
		return nil, fmt.Errorf("invalid forwarded notify argv")
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf("forwarded notify needs an executable")
	}
	return argv, nil
}

func meowNotify(argv []string, knownExecutables ...string) ([]string, bool) {
	if len(argv) < 3 || len(argv) > 5 {
		return nil, false
	}
	executable := path.Base(strings.ReplaceAll(argv[0], `\`, "/"))
	known := executable == "meow" || executable == "meow.exe"
	for _, candidate := range knownExecutables {
		if candidate != "" && candidate == argv[0] {
			known = true
		}
	}
	if !known || argv[1] != "hook" || argv[2] != "codex" {
		return nil, false
	}
	var forwarded []string
	summarySeen := false
	for _, arg := range argv[3:] {
		switch {
		case arg == "--no-summary" && !summarySeen:
			summarySeen = true
		case strings.HasPrefix(arg, ForwardNotifyPrefix) && forwarded == nil:
			var err error
			forwarded, err = DecodeForwardNotify(strings.TrimPrefix(arg, ForwardNotifyPrefix))
			if err != nil {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return forwarded, true
}

// CodexOriginalNotify unwraps our dispatcher, or returns an existing external
// command. This makes reinstall and uninstall independent of local state files.
func CodexOriginalNotify(src []byte, knownExecutables ...string) ([]string, error) {
	var value struct {
		Notify []string `toml:"notify"`
	}
	if err := toml.Unmarshal(src, &value); err != nil {
		return nil, err
	}
	if forwarded, ours := meowNotify(value.Notify, knownExecutables...); ours {
		return forwarded, nil
	}
	if len(value.Notify) > 0 && value.Notify[0] == "" {
		return nil, fmt.Errorf("existing notify has an empty executable")
	}
	return value.Notify, nil
}

// NotifyConflictError means config.toml already runs another notify command.
// Codex supports only one.
type NotifyConflictError struct{ Existing string }

func (e *NotifyConflictError) Error() string { return "config.toml already sets " + e.Existing }

// CodexNotify returns the top-level notify assignment and whether it is Meow's.
// Invalid TOML is left to CodexConfig to report before any file is written.
func CodexNotify(src []byte, knownExecutables ...string) (existing string, ours bool) {
	_, start, end, err := locateNotify(src)
	if err != nil || start < 0 {
		return "", false
	}
	existing = strings.TrimSpace(strings.Join(splitLines(src)[start:end], ""))
	return existing, isMeowNotify(existing, knownExecutables...)
}

// CodexConfig edits only the top-level notify assignment. Full validation and
// AST source ranges keep quoted keys, comments and multiline values unambiguous.
func CodexConfig(src []byte, notify []string, install, force bool, knownExecutables ...string) ([]byte, error) {
	header, start, end, err := locateNotify(src)
	if err != nil {
		return nil, err
	}
	lines := splitLines(src)
	if install && len(notify) > 0 {
		knownExecutables = append([]string{notify[0]}, knownExecutables...)
	}
	if start >= 0 {
		existing := strings.TrimSpace(strings.Join(lines[start:end], ""))
		if !isMeowNotify(existing, knownExecutables...) {
			if !install {
				return src, nil
			}
			if !force {
				return nil, &NotifyConflictError{Existing: existing}
			}
		}
		lines = append(lines[:start:start], lines[end:]...)
		if !install && start < len(lines) && strings.TrimSpace(lines[start]) == "" {
			prevBlank := start == 0 || strings.TrimSpace(lines[start-1]) == ""
			nextHeader := start+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[start+1]), "[")
			if prevBlank || nextHeader {
				lines = append(lines[:start:start], lines[start+1:]...)
			}
		}
	}
	if install {
		newline := "\n"
		if bytes.Contains(src, []byte("\r\n")) {
			newline = "\r\n"
		}
		line := "notify = " + TOMLArray(notify) + newline
		switch {
		case start >= 0:
			lines = slices.Insert(lines, start, line)
		case header < len(lines):
			lines = slices.Insert(lines, header, line, newline)
		default:
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				lines[n-1] += newline
			}
			lines = append(lines, line)
		}
	}
	return []byte(strings.Join(lines, "")), nil
}

func splitLines(src []byte) []string {
	lines := strings.SplitAfter(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// locateNotify returns line offsets of the first table and the root notify.
// The stable decoder validates the entire document before the source parser is
// used; no input after the first table can be silently accepted as invalid TOML.
func locateNotify(src []byte) (header, start, end int, err error) {
	var document map[string]any
	if err := toml.Unmarshal(src, &document); err != nil {
		return 0, -1, -1, fmt.Errorf("config.toml is not valid TOML: %w", err)
	}
	header, start, end = len(splitLines(src)), -1, -1
	var parser unstable.Parser
	parser.Reset(src)
	for parser.NextExpression() {
		node := parser.Expression()
		key := node.Key()
		key.Next() // A validated expression always has at least one key.
		line := bytes.Count(src[:key.Node().Raw.Offset], []byte("\n"))
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			return line, start, end, nil
		}
		if string(key.Node().Data) == "notify" && !key.Next() {
			start = line
			end = bytes.Count(src[:node.Raw.Offset+node.Raw.Length], []byte("\n")) + 1
		}
	}
	return header, start, end, parser.Error()
}

// TOMLArray formats strings as a one-line TOML array of basic strings.
func TOMLArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
