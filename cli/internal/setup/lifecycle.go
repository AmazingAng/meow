package setup

import (
	"fmt"
	"strings"
)

// LifecycleHooks edits a hooks.json/settings.json document without modifying
// foreign handlers. Ownership is an exact generated command prefix, not a
// substring that could occur in a user's shell script.
func LifecycleHooks(src []byte, commands map[string]string, install bool, kinds []string, known ...string) ([]byte, error) {
	return LifecycleHooksMatching(src, commands, nil, install, kinds, known...)
}

// LifecycleHooksMatching restricts selected events without widening foreign groups.
func LifecycleHooksMatching(src []byte, commands, matchers map[string]string, install bool, kinds []string, known ...string) ([]byte, error) {
	members, err := parseObject(src)
	if err != nil {
		return nil, err
	}
	hooks := map[string]any{}
	at := -1
	for i, m := range members {
		if m.key == "hooks" {
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
	}
	for event, value := range hooks {
		groups, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("hooks.%s is not a list", event)
		}
		kept := []any{}
		for _, group := range groups {
			m, ok := group.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("invalid hook group")
			}
			handlers, ok := m["hooks"].([]any)
			if !ok {
				return nil, fmt.Errorf("invalid hook handlers")
			}
			remaining := []any{}
			for _, handler := range handlers {
				h, ok := handler.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("invalid hook handler")
				}
				command, _ := h["command"].(string)
				if !ownedLifecycle(command, kinds, known) {
					remaining = append(remaining, handler)
				}
			}
			if len(remaining) > 0 || len(handlers) == 0 {
				m["hooks"] = remaining
				kept = append(kept, m)
			}
		}
		if len(kept) == 0 && len(groups) > 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if install {
		for event, command := range commands {
			hooks[event] = append(list(hooks[event]), hookGroup(matchers[event], command))
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
		members[at] = member{"hooks", raw, true}
	} else {
		members = append(members, member{"hooks", raw, true})
	}
	return render(members)
}

func ownedLifecycle(command string, kinds, known []string) bool {
	for _, exe := range known {
		if exe == "" {
			continue
		}
		for _, kind := range kinds {
			prefix := ShellQuote(exe) + " hook " + kind
			if command == prefix || command == prefix+" --no-summary" {
				return true
			}
		}
	}
	// Uninstall still recognises a PATH installation without a config record.
	for _, kind := range kinds {
		prefix := "meow hook " + kind
		if command == prefix || strings.TrimSuffix(command, " --no-summary") == prefix {
			return true
		}
	}
	return false
}
