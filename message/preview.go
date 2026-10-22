package message

import (
	"bufio"
	"bytes"
	"encoding/json"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	markdownhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
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
		case ast.KindFencedCodeBlock, ast.KindCodeBlock:
			return ast.WalkSkipChildren, nil
		case ast.KindHTMLBlock:
			// Foundation presents raw HTML literally. Preserve its contents as
			// text too; skipping the node can erase the entire notification.
			n := node.(*ast.HTMLBlock)
			value := string(n.Lines().Value(source))
			if n.HasClosure() {
				value += string(n.ClosureLine.Value(source))
			}
			blocks = append(blocks, value)
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
	return strings.TrimSpace(strings.Join(blocks, "\n"))
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
			if n.IsRaw() {
				result.Write(value)
			} else {
				result.WriteString(markdownText(value))
			}
			if n.SoftLineBreak() || n.HardLineBreak() {
				result.WriteByte('\n')
			}
		case *ast.String:
			if n.IsCode() || n.IsRaw() {
				result.Write(n.Value)
			} else {
				result.WriteString(markdownText(n.Value))
			}
		case *ast.AutoLink:
			result.Write(n.Label(source))
		case *ast.RawHTML:
			result.Write(n.Segments.Value(source))
		}
		return ast.WalkContinue, nil
	})
	return result.String()
}

func markdownText(source []byte) string {
	// Goldmark resolves escapes and references in one pass, without interpreting
	// their results again. Undo only the HTML escaping added by that writer;
	// for example, both \&amp; and &#38;amp; must remain the literal "&amp;".
	var escaped bytes.Buffer
	writer := bufio.NewWriter(&escaped)
	markdownhtml.DefaultWriter.Write(writer, source)
	_ = writer.Flush() // bytes.Buffer writes cannot fail.
	return html.UnescapeString(escaped.String())
}
