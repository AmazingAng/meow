// Package setup edits agent configuration files for "meow setup".
//
// The editing functions are pure: they take the current file content and
// return the new content, so the command can show the changes before
// writing anything.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
)

// ClaudeNotifyMatcher selects the Notification events that need the user:
// a permission prompt, or an MCP input form, left waiting for a few seconds.
const ClaudeNotifyMatcher = "permission_prompt|elicitation_dialog"

var meowClaudeHook = regexp.MustCompile(`meow\S*\s+hook\s+claude-(stop|notify)\b`)

// member is one top-level key of settings.json. Values meow doesn't touch
// keep their original bytes, so the user's formatting survives.
type member struct {
	key   string
	val   json.RawMessage
	fresh bool // re-encoded by meow; indented when written
}

// ClaudeSettings adds (install) or removes meow's hooks in a Claude Code
// settings.json. Other settings and hooks are kept, and top-level keys keep
// their order. Hooks are async so they never hold up the session.
//
// knownExecutables are meow binaries installed earlier; hooks that run one of
// them are treated as meow's even if the file isn't named meow.
func ClaudeSettings(src []byte, stopCmd, notifyCmd string, install bool, knownExecutables ...string) ([]byte, error) {
	members, err := parseObject(src)
	if err != nil {
		return nil, fmt.Errorf("settings.json is not valid JSON: %w", err)
	}
	at := -1
	hooks := map[string]any{}
	for i, m := range members {
		if m.key != "hooks" {
			continue
		}
		at = i
		v, err := decode(m.val)
		if err != nil {
			return nil, err
		}
		if v == nil {
			continue // "hooks": null
		}
		h, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New(`"hooks" in settings.json is not an object`)
		}
		hooks = h
	}
	for _, ev := range []string{"Stop", "Notification"} {
		if v, ok := hooks[ev]; ok {
			if _, ok := v.([]any); !ok {
				return nil, fmt.Errorf(`"hooks.%s" in settings.json is not a list`, ev)
			}
		}
	}

	removed := removeMeowHooks(hooks, knownExecutables)
	switch {
	case !install && !removed:
		return src, nil
	case !install && len(hooks) == 0 && at >= 0:
		// meow's hooks were the only ones; drop the key it added.
		return render(append(members[:at:at], members[at+1:]...))
	}
	if install {
		hooks["Stop"] = append(list(hooks["Stop"]), hookGroup("", stopCmd))
		hooks["Notification"] = append(list(hooks["Notification"]), hookGroup(ClaudeNotifyMatcher, notifyCmd))
	}
	raw, err := marshal(hooks)
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		members[at] = member{"hooks", raw, true}
	} else {
		members = append(members, member{"hooks", raw, true})
	}
	return render(members)
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func hookGroup(matcher, cmd string) map[string]any {
	g := map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": cmd, "async": true}},
	}
	if matcher != "" {
		g["matcher"] = matcher
	}
	return g
}

func isMeowHook(h any, knownExecutables []string) bool {
	m, ok := h.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	if meowClaudeHook.MatchString(cmd) {
		return true
	}
	for _, exe := range knownExecutables {
		if exe == "" {
			continue
		}
		for _, kind := range []string{"claude-stop", "claude-notify"} {
			if strings.HasPrefix(cmd, ShellQuote(exe)+" hook "+kind) {
				return true
			}
		}
	}
	return false
}

// removeMeowHooks deletes handlers added by meow from every event, dropping
// groups and events that become empty because of it. It reports whether
// anything was removed.
func removeMeowHooks(hooks map[string]any, known []string) (changed bool) {
	for ev, v := range hooks {
		groups, ok := v.([]any)
		if !ok {
			continue
		}
		removed := false
		var kept []any
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			handlers, ok2 := gm["hooks"].([]any)
			if !ok || !ok2 {
				kept = append(kept, g)
				continue
			}
			var keptHandlers []any
			for _, h := range handlers {
				if isMeowHook(h, known) {
					removed = true
				} else {
					keptHandlers = append(keptHandlers, h)
				}
			}
			switch {
			case len(keptHandlers) == len(handlers):
				kept = append(kept, g)
			case len(keptHandlers) > 0:
				gm["hooks"] = keptHandlers
				kept = append(kept, gm)
			}
		}
		switch {
		case !removed:
			continue
		case len(kept) == 0:
			delete(hooks, ev)
		default:
			hooks[ev] = kept
		}
		changed = true
	}
	return changed
}

// EquivalentJSON reports whether a and b hold the same JSON value; empty
// input counts as an empty object.
func EquivalentJSON(a, b []byte) bool {
	va, ea := decodeOrEmpty(a)
	vb, eb := decodeOrEmpty(b)
	return ea == nil && eb == nil && reflect.DeepEqual(va, vb)
}

func decodeOrEmpty(b []byte) (any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, nil
	}
	return decode(b)
}

func decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep numbers exactly as written
	var v any
	err := dec.Decode(&v)
	return v, err
}

func marshal(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// parseObject splits a JSON object into its members, in file order.
func parseObject(src []byte) ([]member, error) {
	if len(bytes.TrimSpace(src)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(src))
	if t, err := dec.Token(); err != nil {
		return nil, err
	} else if t != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	var ms []member
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := t.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		ms = append(ms, member{key: key, val: raw})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return ms, nil
}

func render(ms []member) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString("\n  ")
		k, err := marshal(m.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteString(": ")
		if !m.fresh {
			b.Write(m.val)
		} else if err := json.Indent(&b, m.val, "  ", "  "); err != nil {
			return nil, err
		}
	}
	if len(ms) > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.Bytes(), nil
}
