package ui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	goldtext "github.com/yuin/goldmark/text"
)

var mdGold = goldmark.New()

// renderMarkdown parses src as Markdown and returns tview-tagged lines wrapped
// to maxW visible columns. Each element is one logical display line.
func renderMarkdown(src string, maxW int) []string {
	if maxW <= 0 {
		maxW = 40
	}
	source := []byte(src)
	reader := goldtext.NewReader(source)
	doc := mdGold.Parser().Parse(reader)

	var lines []string
	renderBlocks(doc, source, maxW, &lines)
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// ─── block rendering ─────────────────────────────────────────────────────────

func renderBlocks(node ast.Node, source []byte, maxW int, out *[]string) {
	for n := node.FirstChild(); n != nil; n = n.NextSibling() {
		renderBlock(n, source, maxW, out)
	}
}

func renderBlock(n ast.Node, source []byte, maxW int, out *[]string) {
	switch n.Kind() {
	case ast.KindParagraph, ast.KindTextBlock:
		spans := collectSpans(n, source, mdStyle{})
		*out = append(*out, wrapSpans(spans, maxW)...)

	case ast.KindHeading:
		h := n.(*ast.Heading)
		prefix := strings.Repeat("#", h.Level) + " "
		plain := spansPlain(collectSpans(n, source, mdStyle{}))
		ac := tviewColor(Theme.Accent)
		wrapped := tview.WordWrap(tview.Escape(prefix+plain), maxW)
		for _, l := range wrapped {
			*out = append(*out, fmt.Sprintf("[%s][::b]%s[-:-:-]", ac, l))
		}

	case ast.KindFencedCodeBlock:
		segs := n.(*ast.FencedCodeBlock).Lines()
		cc := tviewColor(Theme.ShellColor)
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			line := strings.TrimRight(string(seg.Value(source)), "\n")
			*out = append(*out, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(line)))
		}

	case ast.KindCodeBlock:
		segs := n.(*ast.CodeBlock).Lines()
		cc := tviewColor(Theme.ShellColor)
		for i := 0; i < segs.Len(); i++ {
			seg := segs.At(i)
			line := strings.TrimRight(string(seg.Value(source)), "\n")
			*out = append(*out, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(line)))
		}

	case ast.KindList:
		renderList(n.(*ast.List), source, maxW, out, 0)

	case ast.KindBlockquote:
		mc := tviewColor(Theme.Muted)
		var inner []string
		renderBlocks(n, source, maxW-2, &inner)
		for _, l := range inner {
			*out = append(*out, fmt.Sprintf("[%s]▎[-:-:-] %s", mc, l))
		}

	case ast.KindThematicBreak:
		mc := tviewColor(Theme.Muted)
		*out = append(*out, fmt.Sprintf("[%s]%s[-:-:-]", mc, strings.Repeat("─", maxW)))

	default:
		renderBlocks(n, source, maxW, out)
	}
}

func renderList(list *ast.List, source []byte, maxW int, out *[]string, depth int) {
	indent := strings.Repeat("  ", depth)
	num := list.Start
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		var bullet string
		if list.IsOrdered() {
			bullet = fmt.Sprintf("%d. ", num)
			num++
		} else {
			bullet = "• "
		}
		prefix := indent + bullet
		cont := indent + strings.Repeat(" ", len([]rune(bullet)))
		innerW := maxW - len([]rune(prefix))
		if innerW < 10 {
			innerW = 10
		}

		var itemLines []string
		renderBlocks(item, source, innerW, &itemLines)
		for i, l := range itemLines {
			if i == 0 {
				*out = append(*out, prefix+l)
			} else {
				*out = append(*out, cont+l)
			}
		}
	}
}

// ─── inline span collection ───────────────────────────────────────────────────

// mdStyle carries the active inline formatting down the inline AST.
type mdStyle struct {
	bold   bool
	italic bool
	code   bool
}

// openTag returns the tview style tag to emit before text in this style.
// Always begins with [-:-:-] to prevent style bleeding from previous segments.
func (s mdStyle) openTag() string {
	if s.code {
		return fmt.Sprintf("[-:-:-][%s]", tviewColor(Theme.ShellColor))
	}
	attrs := ""
	if s.bold {
		attrs += "b"
	}
	if s.italic {
		attrs += "i"
	}
	if attrs == "" {
		return "[-:-:-]"
	}
	return "[-:-:-][::" + attrs + "]"
}

type mdSpan struct {
	text  string
	style mdStyle
}

func collectSpans(node ast.Node, source []byte, style mdStyle) []mdSpan {
	var spans []mdSpan
	for c := node.FirstChild(); c != nil; c = c.NextSibling() {
		spans = append(spans, nodeSpans(c, source, style)...)
	}
	return spans
}

func nodeSpans(node ast.Node, source []byte, style mdStyle) []mdSpan {
	switch node.Kind() {
	case ast.KindText:
		t := node.(*ast.Text)
		text := string(t.Value(source))
		if t.SoftLineBreak() {
			text += " "
		}
		sp := mdSpan{text: text, style: style}
		if t.HardLineBreak() {
			return []mdSpan{sp, {text: "\n", style: mdStyle{}}}
		}
		return []mdSpan{sp}

	case ast.KindString:
		s := node.(*ast.String)
		return []mdSpan{{text: string(s.Value), style: style}}

	case ast.KindCodeSpan:
		return collectSpans(node, source, mdStyle{code: true})

	case ast.KindEmphasis:
		em := node.(*ast.Emphasis)
		s2 := style
		if em.Level >= 2 {
			s2.bold = true
		} else {
			s2.italic = true
		}
		return collectSpans(node, source, s2)

	case ast.KindLink, ast.KindImage:
		return collectSpans(node, source, style)

	case ast.KindAutoLink:
		al := node.(*ast.AutoLink)
		return []mdSpan{{text: string(al.Label(source)), style: mdStyle{code: true}}}

	case ast.KindRawHTML:
		return nil

	default:
		return collectSpans(node, source, style)
	}
}

func spansPlain(spans []mdSpan) string {
	var sb strings.Builder
	for _, sp := range spans {
		sb.WriteString(sp.text)
	}
	return sb.String()
}

// ─── span-aware word wrap ─────────────────────────────────────────────────────

type styledRune struct {
	r     rune
	style mdStyle
}

// wrapSpans wraps the given spans into lines of at most maxW visible columns,
// breaking on spaces and CJK character boundaries, hard-breaking overlong
// tokens. Each returned line is a tview-tagged string.
func wrapSpans(spans []mdSpan, maxW int) []string {
	if maxW <= 0 {
		maxW = 40
	}

	var chars []styledRune
	for _, sp := range spans {
		for _, r := range []rune(sp.text) {
			chars = append(chars, styledRune{r, sp.style})
		}
	}

	if len(chars) == 0 {
		return []string{""}
	}

	// sentinel: differs from every valid mdStyle so the first rune always
	// emits a style tag, preventing bleed from the previous rendered line.
	sentinel := mdStyle{code: true, bold: true, italic: true}

	emitLine := func(from, to int) string {
		if to <= from {
			return ""
		}
		var sb strings.Builder
		cur := sentinel
		for i := from; i < to; i++ {
			if chars[i].style != cur {
				sb.WriteString(chars[i].style.openTag())
				cur = chars[i].style
			}
			if chars[i].r == '[' {
				sb.WriteString("[[]")
			} else {
				sb.WriteRune(chars[i].r)
			}
		}
		return sb.String()
	}

	var lines []string
	start := 0
	lineW := 0
	lastSpace := -1

	for i, ch := range chars {
		if ch.r == '\n' {
			lines = append(lines, emitLine(start, i))
			start = i + 1
			lineW = 0
			lastSpace = -1
			continue
		}

		rW := tview.TaggedStringWidth(string(ch.r))

		// CJK: break before this rune if adding it would overflow.
		if isCJKRune(ch.r) && lineW > 0 && lineW+rW > maxW {
			lines = append(lines, emitLine(start, i))
			start = i
			lineW = 0
			lastSpace = -1
		}

		if ch.r == ' ' {
			lastSpace = i
		}

		lineW += rW

		if lineW > maxW {
			if lastSpace > start {
				lines = append(lines, emitLine(start, lastSpace))
				start = lastSpace + 1 // skip the space
				lineW = 0
				for j := start; j <= i; j++ {
					lineW += tview.TaggedStringWidth(string(chars[j].r))
				}
				lastSpace = -1
			} else {
				lines = append(lines, emitLine(start, i))
				start = i
				lineW = rW
				lastSpace = -1
			}
		}
	}

	lines = append(lines, emitLine(start, len(chars)))

	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
