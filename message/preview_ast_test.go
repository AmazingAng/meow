package message

import (
	"testing"

	"github.com/yuin/goldmark/ast"
)

// String nodes carry generated inline text rather than source-backed segments.
// Decode escapes only for prose: code and raw values must remain literal.
func TestInlineGeneratedTextPreservesLiteralIntent(t *testing.T) {
	for _, tc := range []struct {
		name, value, want string
		code, raw         bool
	}{
		{name: "prose entity", value: "A &amp; B", want: "A & B"},
		{name: "escaped prose", value: `\&amp;`, want: "&amp;"},
		{name: "code entity", value: "&amp;", want: "&amp;", code: true},
		{name: "raw entity", value: "&amp;", want: "&amp;", raw: true},
		{name: "code escape", value: `\*literal\*`, want: `\*literal\*`, code: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paragraph := ast.NewParagraph()
			node := ast.NewString([]byte(tc.value))
			node.SetCode(tc.code)
			node.SetRaw(tc.raw)
			paragraph.AppendChild(paragraph, node)
			if got := inlineText(paragraph, nil); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}
