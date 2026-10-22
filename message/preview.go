package message

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

const MaxPreviewRunes = 180
const MaxPreviewLines = 3

// Preview condenses banners only. The original body is preserved for the inbox
// and Markdown detail renderer. Swift uses the same cross-language fixtures.
func Preview(body string) string {
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return ""
	}
	if (strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[")) && json.Valid([]byte(text)) {
		return ""
	}
	text = plainMarkdown(text)
	lines := strings.Split(text, "\n")
	if len(lines) > MaxPreviewLines {
		text = strings.Join(lines[:MaxPreviewLines], "\n") + "…"
	}
	return Truncate(text, MaxPreviewRunes)
}

func Truncate(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit-1]) + "…"
}

// Parse the complete body before shortening it: truncating Markdown first can
// leave an unmatched opening delimiter in the system banner.
func plainMarkdown(markdown string) string {
	source := []byte(markdown)
	document := goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough)).Parser().Parse(text.NewReader(source))
	var blocks []string
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindFencedCodeBlock, ast.KindCodeBlock, ast.KindHTMLBlock:
			return ast.WalkSkipChildren, nil
		case ast.KindParagraph, ast.KindHeading, ast.KindTextBlock:
			if value := strings.TrimSpace(inlineText(node, source)); value != "" {
				blocks = append(blocks, value)
			}
			return ast.WalkSkipChildren, nil
		case extast.KindTableHeader, extast.KindTableRow:
			var cells []string
			for cell := node.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if value := strings.TrimSpace(inlineText(cell, source)); value != "" {
					cells = append(cells, value)
				}
			}
			if len(cells) > 0 {
				blocks = append(blocks, strings.Join(cells, " · "))
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.Join(blocks, "\n")
}

func inlineText(node ast.Node, source []byte) string {
	var result strings.Builder
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := child.(type) {
		case *ast.Text:
			value := n.Segment.Value(source)
			if !n.IsRaw() {
				value = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(value)))
			}
			result.Write(value)
			if n.SoftLineBreak() || n.HardLineBreak() {
				result.WriteByte('\n')
			}
		case *ast.String:
			result.Write(n.Value)
		case *ast.AutoLink:
			result.Write(n.Label(source))
		}
		return ast.WalkContinue, nil
	})
	return result.String()
}
