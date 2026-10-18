package setup

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

var tomlNotify = regexp.MustCompile(`^notify\s*=`)

// isMeowNotify reports whether a notify assignment runs "meow hook codex":
// its argv has "hook" followed by "codex", ignoring comments and line breaks.
func isMeowNotify(assignment string) bool {
	argv := tomlStrings(assignment)
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "hook" && argv[i+1] == "codex" {
			return true
		}
	}
	return false
}

// tomlStrings returns the basic and literal string values in s, skipping
// comments. Escapes other than \" and \\ are kept as written, which is enough
// to compare the plain words meow writes.
func tomlStrings(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '"', '\'':
			var b strings.Builder
			for i++; i < len(s) && s[i] != c && s[i] != '\n'; i++ {
				if c == '"' && s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
				}
				b.WriteByte(s[i])
			}
			out = append(out, b.String())
		}
	}
	return out
}

// NotifyConflictError means config.toml already runs another notify command.
// Codex supports only one.
type NotifyConflictError struct{ Existing string }

func (e *NotifyConflictError) Error() string { return "config.toml already sets " + e.Existing }

// CodexNotify returns the top-level notify assignment in a Codex config.toml
// ("" if there is none) and whether meow installed it.
func CodexNotify(src []byte) (existing string, ours bool) {
	lines := splitLines(src)
	_, start, end := locateNotify(lines)
	if start < 0 {
		return "", false
	}
	existing = strings.TrimSpace(strings.Join(lines[start:end], ""))
	return existing, isMeowNotify(existing)
}

// CodexConfig sets (install) or removes meow's top-level notify command in a
// Codex config.toml, leaving every other line as it is. A notify command that
// meow did not install is a conflict unless force is set; uninstall leaves it alone.
func CodexConfig(src []byte, notify []string, install, force bool) ([]byte, error) {
	lines := splitLines(src)
	header, start, end := locateNotify(lines)
	if start >= 0 {
		existing := strings.TrimSpace(strings.Join(lines[start:end], ""))
		if !isMeowNotify(existing) {
			if !install {
				return src, nil
			}
			if !force {
				return nil, &NotifyConflictError{Existing: existing}
			}
		}
		lines = append(lines[:start:start], lines[end:]...)
		if !install && start < len(lines) && strings.TrimSpace(lines[start]) == "" {
			// Drop the blank line install put between notify and the first table.
			prevBlank := start == 0 || strings.TrimSpace(lines[start-1]) == ""
			nextHeader := start+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[start+1]), "[")
			if prevBlank || nextHeader {
				lines = append(lines[:start:start], lines[start+1:]...)
			}
		}
	}
	if install {
		line := "notify = " + TOMLArray(notify) + "\n"
		switch {
		case start >= 0: // keep its place in the file
			lines = slices.Insert(lines, start, line)
		case header < len(lines): // top-level keys must come before the first table
			lines = slices.Insert(lines, header, line, "\n")
		default:
			if n := len(lines); n > 0 && !strings.HasSuffix(lines[n-1], "\n") {
				lines[n-1] += "\n"
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

// locateNotify finds the first table header and the lines [start, end) of a
// top-level notify assignment (start is -1 when there is none).
func locateNotify(lines []string) (header, start, end int) {
	header, start, end = len(lines), -1, -1
	open := "" // delimiter of an unterminated multi-line string
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if open != "" {
			if strings.Count(t, open)%2 == 1 {
				open = ""
			}
			continue
		}
		if strings.HasPrefix(t, "[") {
			header = i
			return
		}
		if start < 0 && tomlNotify.MatchString(t) {
			start, end = i, i+1
			for depth := bracketDepth(t); depth > 0 && end < len(lines); end++ {
				depth += bracketDepth(lines[end])
			}
			i = end - 1
			continue
		}
		for _, d := range []string{`"""`, `'''`} {
			if strings.Count(t, d)%2 == 1 {
				open = d
			}
		}
	}
	return
}

// bracketDepth counts unquoted [ minus ] on a line, stopping at a comment.
func bracketDepth(s string) int {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return depth
		case c == '[':
			depth++
		case c == ']':
			depth--
		}
	}
	return depth
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
