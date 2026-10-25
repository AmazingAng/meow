package setup

import "fmt"

// CursorHooks edits a Cursor hooks.json document. Cursor commands are a flat
// list under hooks.<event>, not Claude's nested matcher groups. Ownership is
// an exact generated command, so a user's script that mentions the same words
// is left alone.
func CursorHooks(src []byte, command string, install bool, known ...string) ([]byte, error) {
	members, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	hooks := map[string]any{}
	at := -1
	for i, m := range members {
		if m.key != "hooks" {
			continue
		}
		at = i
		value, e := decode(m.val)
		if e != nil {
			return nil, e
		}
		if value == nil {
			continue
		}
		var ok bool
		hooks, ok = value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("hooks is not an object")
		}
	}
	for event, value := range hooks {
		entries, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s is not a list", event)
		}
		kept := make([]any, 0, len(entries))
		for _, entry := range entries {
			hook, ok := entry.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid hook")
			}
			cmd, _ := hook["command"].(string)
			if !ownedCursor(cmd, known) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if install {
		hooks["stop"] = append(list(hooks["stop"]), map[string]any{"command": command, "timeout": 30})
		if !hasMember(members, "version") {
			raw, err := marshal(1)
			if err != nil {
				return nil, err
			}
			members = append([]member{{key: "version", val: raw, fresh: true}}, members...)
			if at >= 0 {
				at++
			}
		}
	}
	if len(hooks) == 0 && at >= 0 {
		return render(append(members[:at:at], members[at+1:]...))
	}
	if len(hooks) == 0 {
		return src, nil
	}
	raw, err := marshal(hooks)
	if err != nil {
		return nil, err
	}
	if at >= 0 {
		members[at] = member{key: "hooks", val: raw, fresh: true}
	} else {
		members = append(members, member{key: "hooks", val: raw, fresh: true})
	}
	return render(members)
}

func hasMember(members []member, key string) bool {
	for _, m := range members {
		if m.key == key {
			return true
		}
	}
	return false
}

func ownedCursor(command string, known []string) bool {
	for _, exe := range known {
		if exe == "" {
			continue
		}
		prefix := ShellQuote(exe) + " hook cursor-stop"
		if command == prefix || command == prefix+" --no-summary" {
			return true
		}
	}
	prefix := "meow hook cursor-stop"
	return command == prefix || command == prefix+" --no-summary"
}
