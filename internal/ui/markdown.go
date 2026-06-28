package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	goldtext "github.com/yuin/goldmark/text"
)

var mdGold = goldmark.New(
	goldmark.WithExtensions(
		extension.Table,
		extension.Strikethrough,
	),
)

// ─── block IR (width-independent) ────────────────────────────────────────────

// mdBlock is a parsed, width-independent markdown block. renderLines re-wraps
// it to the given visible column width — called on every resize, never re-parses.
type mdBlock interface {
	renderLines(maxW int) []string
}

type paragraphBlock struct{ spans []mdSpan }
type headingBlock struct {
	level int
	spans []mdSpan
}
type codeBlock struct {
	lines []string
	lang  string
}
type thematicBreakBlock struct{}
type blockquoteBlock struct{ items []mdBlock }
type listBlock struct {
	ordered bool
	start   int
	items   []listItemBlock
}
type listItemBlock struct{ blocks []mdBlock }
type tableBlock struct {
	alignments []extast.Alignment
	header     [][]mdSpan
	rows       [][][]mdSpan
}
type groupBlock struct{ items []mdBlock } // fallback for unknown block nodes

// ─── renderLines implementations ─────────────────────────────────────────────

func (b *paragraphBlock) renderLines(maxW int) []string {
	return wrapSpans(b.spans, maxW)
}

func (b *headingBlock) renderLines(maxW int) []string {
	prefix := strings.Repeat("#", b.level) + " "
	plain := spansPlain(b.spans)
	ac := tviewColor(Theme.Accent)
	wrapped := tview.WordWrap(tview.Escape(prefix+plain), maxW)
	out := make([]string, len(wrapped))
	for i, l := range wrapped {
		out[i] = fmt.Sprintf("[%s][::b]%s[-:-:-]", ac, l)
	}
	return out
}

func (b *codeBlock) renderLines(maxW int) []string {
	bc := tviewColor(Theme.Border)
	mc := tviewColor(Theme.Muted)
	innerW := max(1, maxW-4) // │·content·│ → 2 borders + 2 spaces

	// top border: ┌──── lang ─┐  (label on right, like chat name but mirrored)
	topBorder := func() string {
		lang := strings.TrimSpace(b.lang)
		// " lang ─┐" visible width
		labelW := len([]rune(lang)) + 4
		fill := maxW - 1 - labelW
		if lang == "" || fill < 1 {
			return fmt.Sprintf("[%s]┌%s┐[-:-:-]", bc, strings.Repeat("─", maxW-2))
		}
		return fmt.Sprintf("[%s]┌%s [%s]%s [%s]─┐[-:-:-]",
			bc, strings.Repeat("─", fill), mc, lang, bc)
	}
	botBorder := fmt.Sprintf("[%s]└%s┘[-:-:-]", bc, strings.Repeat("─", maxW-2))

	var inner []string
	if b.lang != "" {
		inner = b.renderHighlighted(innerW)
	} else {
		inner = b.renderPlain(innerW)
	}

	out := make([]string, 0, len(inner)+2)
	out = append(out, topBorder())
	for _, line := range inner {
		visW := tview.TaggedStringWidth(line)
		pad := strings.Repeat(" ", max(0, innerW-visW))
		out = append(out, fmt.Sprintf("[%s]│[-:-:-] %s%s [%s]│[-:-:-]", bc, line, pad, bc))
	}
	out = append(out, botBorder)
	return out
}

func (b *codeBlock) renderPlain(innerW int) []string {
	cc := tviewColor(Theme.CodeColor)
	var out []string
	for _, line := range b.lines {
		// Measure after escaping: raw code may contain "[word]" sequences that
		// tview's tag parser treats as zero-width style tags, so
		// TaggedStringWidth(line) would undercount and skip needed wrapping.
		escaped := tview.Escape(line)
		if innerW <= 0 || tview.TaggedStringWidth(escaped) <= innerW {
			out = append(out, fmt.Sprintf("[%s]%s[-:-:-]", cc, escaped))
			continue
		}
		// Wrap by iterating runes of the unescaped line; individual runes are
		// safe to measure (a lone '[' never forms a complete tag).
		runes := []rune(line)
		start, lineW := 0, 0
		for i, r := range runes {
			rW := tview.TaggedStringWidth(string(r))
			if lineW+rW > innerW {
				out = append(out, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(string(runes[start:i]))))
				start, lineW = i, 0
			}
			lineW += rW
		}
		if start < len(runes) {
			out = append(out, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(string(runes[start:]))))
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// renderHighlighted uses chroma to produce per-token colored tview lines.
func (b *codeBlock) renderHighlighted(maxW int) []string {
	// tokenize the entire block at once so chroma tracks state across lines
	// (e.g. multi-line strings, block comments)
	content := strings.Join(b.lines, "\n")
	tokens := tokenizeForMarkdown(content, b.lang)

	// split at \n into per-logical-line token slices
	var logicalLines [][]diffStyledRune
	lineStart := 0
	for i, sr := range tokens {
		if sr.R == '\n' {
			logicalLines = append(logicalLines, tokens[lineStart:i])
			lineStart = i + 1
		}
	}
	logicalLines = append(logicalLines, tokens[lineStart:])

	var out []string
	for _, lt := range logicalLines {
		out = append(out, wrapStyledRunesToTagged(lt, maxW)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// styledRunesToTagged converts diffStyledRune slices to a tview-tagged string.
func styledRunesToTagged(runes []diffStyledRune) string {
	if len(runes) == 0 {
		return ""
	}
	var sb strings.Builder
	first := true
	var cur tcell.Color
	for _, sr := range runes {
		fg, _, _ := sr.Style.Decompose()
		if first || fg != cur {
			r8, g8, b8 := fg.RGB()
			fmt.Fprintf(&sb, "[%s]", colorHex(r8, g8, b8))
			cur = fg
			first = false
		}
		if sr.R == '[' {
			sb.WriteString("[[]")
		} else {
			sb.WriteRune(sr.R)
		}
	}
	sb.WriteString("[-:-:-]")
	return sb.String()
}

// wrapStyledRunesToTagged hard-wraps a highlighted line at maxW, producing tview-tagged strings.
func wrapStyledRunesToTagged(runes []diffStyledRune, maxW int) []string {
	if len(runes) == 0 {
		return []string{""}
	}
	if maxW <= 0 {
		return []string{styledRunesToTagged(runes)}
	}
	var out []string
	start := 0
	lineW := 0
	for i, sr := range runes {
		rW := tview.TaggedStringWidth(string(sr.R))
		if lineW+rW > maxW && lineW > 0 {
			out = append(out, styledRunesToTagged(runes[start:i]))
			start = i
			lineW = 0
		}
		lineW += rW
	}
	out = append(out, styledRunesToTagged(runes[start:]))
	return out
}

func (b *thematicBreakBlock) renderLines(maxW int) []string {
	mc := tviewColor(Theme.Muted)
	return []string{fmt.Sprintf("[%s]%s[-:-:-]", mc, strings.Repeat("─", maxW))}
}

func (b *blockquoteBlock) renderLines(maxW int) []string {
	mc := tviewColor(Theme.Muted)
	innerW := maxW - 2
	if innerW < 1 {
		innerW = 1
	}
	var inner []string
	for _, item := range b.items {
		inner = append(inner, item.renderLines(innerW)...)
	}
	out := make([]string, len(inner))
	for i, l := range inner {
		out[i] = fmt.Sprintf("[%s]▎[-:-:-] %s", mc, l)
	}
	return out
}

func (b *listBlock) renderLines(maxW int) []string {
	num := b.start
	var out []string
	for _, item := range b.items {
		var bullet string
		if b.ordered {
			bullet = fmt.Sprintf("%d. ", num)
			num++
		} else {
			bullet = "• "
		}
		bulletW := len([]rune(bullet))
		innerW := maxW - bulletW
		if innerW < 10 {
			innerW = 10
		}
		cont := strings.Repeat(" ", bulletW)

		var itemLines []string
		for _, block := range item.blocks {
			itemLines = append(itemLines, block.renderLines(innerW)...)
		}
		for i, l := range itemLines {
			if i == 0 {
				out = append(out, bullet+l)
			} else {
				out = append(out, cont+l)
			}
		}
	}
	return out
}

func (b *tableBlock) renderLines(maxW int) []string {
	var out []string
	renderTableBlock(b, maxW, &out)
	return out
}

func (b *groupBlock) renderLines(maxW int) []string {
	var out []string
	for _, item := range b.items {
		out = append(out, item.renderLines(maxW)...)
	}
	return out
}

// ─── parsing (goldmark AST → []mdBlock) ──────────────────────────────────────

// parseMarkdown parses src into a width-independent block list.
func parseMarkdown(src string) []mdBlock {
	source := []byte(src)
	reader := goldtext.NewReader(source)
	doc := mdGold.Parser().Parse(reader)
	return parseBlockChildren(doc, source)
}

func parseBlockChildren(node ast.Node, source []byte) []mdBlock {
	var blocks []mdBlock
	for n := node.FirstChild(); n != nil; n = n.NextSibling() {
		if b := parseOneBlock(n, source); b != nil {
			blocks = append(blocks, b)
		}
	}
	return blocks
}

func parseOneBlock(n ast.Node, source []byte) mdBlock {
	switch n.Kind() {
	case ast.KindParagraph, ast.KindTextBlock:
		return &paragraphBlock{spans: collectSpans(n, source, mdStyle{})}

	case ast.KindHeading:
		h := n.(*ast.Heading)
		return &headingBlock{level: h.Level, spans: collectSpans(n, source, mdStyle{})}

	case ast.KindFencedCodeBlock:
		fcb := n.(*ast.FencedCodeBlock)
		return parseCodeLines(fcb.Lines(), source, string(fcb.Language(source)))

	case ast.KindCodeBlock:
		return parseCodeLines(n.(*ast.CodeBlock).Lines(), source, "")

	case ast.KindList:
		return parseList(n.(*ast.List), source)

	case ast.KindBlockquote:
		return &blockquoteBlock{items: parseBlockChildren(n, source)}

	case ast.KindThematicBreak:
		return &thematicBreakBlock{}

	case extast.KindTable:
		return parseTable(n.(*extast.Table), source)

	default:
		children := parseBlockChildren(n, source)
		if len(children) == 0 {
			return nil
		}
		return &groupBlock{items: children}
	}
}

func parseCodeLines(segs *goldtext.Segments, source []byte, lang string) *codeBlock {
	lines := make([]string, segs.Len())
	for i := 0; i < segs.Len(); i++ {
		seg := segs.At(i)
		lines[i] = strings.TrimRight(string(seg.Value(source)), "\n")
	}
	return &codeBlock{lines: lines, lang: lang}
}

func parseList(list *ast.List, source []byte) *listBlock {
	b := &listBlock{ordered: list.IsOrdered(), start: list.Start}
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		var itemBlocks []mdBlock
		for c := item.FirstChild(); c != nil; c = c.NextSibling() {
			if b := parseOneBlock(c, source); b != nil {
				itemBlocks = append(itemBlocks, b)
			}
		}
		b.items = append(b.items, listItemBlock{blocks: itemBlocks})
	}
	return b
}

func parseTable(tbl *extast.Table, source []byte) *tableBlock {
	b := &tableBlock{alignments: tbl.Alignments}
	for row := tbl.FirstChild(); row != nil; row = row.NextSibling() {
		var cells [][]mdSpan
		for c := row.FirstChild(); c != nil; c = c.NextSibling() {
			cells = append(cells, collectSpans(c, source, mdStyle{}))
		}
		switch row.Kind() {
		case extast.KindTableHeader:
			b.header = cells
		case extast.KindTableRow:
			b.rows = append(b.rows, cells)
		}
	}
	return b
}

// ─── table rendering ──────────────────────────────────────────────────────────

func renderTableBlock(tbl *tableBlock, maxW int, out *[]string) {
	numCols := len(tbl.alignments)
	if numCols == 0 {
		numCols = len(tbl.header)
	}
	if numCols == 0 {
		return
	}

	// Phase 1: compute natural column widths and per-column minimum widths.
	// minColW[i] = longest single word in column i — word wrapping never needs
	// to hard-break within a word as long as colW[i] >= minColW[i].
	colW := make([]int, numCols)
	minColW := make([]int, numCols)

	measureCol := func(i int, spans []mdSpan) {
		if i >= numCols {
			return
		}
		plain := spansPlain(spans)
		tagged := spansToTagged(spans)
		if w := tview.TaggedStringWidth(tagged); w > colW[i] {
			colW[i] = w
		}
		for _, word := range strings.Fields(plain) {
			if w := tview.TaggedStringWidth(word); w > minColW[i] {
				minColW[i] = w
			}
		}
		if minColW[i] == 0 {
			minColW[i] = 1
		}
	}

	for i, spans := range tbl.header {
		measureCol(i, spans)
	}
	for _, row := range tbl.rows {
		for i, spans := range row {
			measureCol(i, spans)
		}
	}

	// Phase 2: scale columns if table exceeds maxW.
	// Total width = 2 + 3*numCols + sum(colW).
	if maxW > 0 {
		sumColW := 0
		for _, w := range colW {
			sumColW += w
		}
		if 2+3*numCols+sumColW > maxW {
			available := maxW - 2 - 3*numCols
			if available < numCols {
				available = numCols
			}
			totalMinW := 0
			for _, w := range minColW {
				totalMinW += w
			}
			if available >= totalMinW {
				// Distribute space: each column gets at least minColW, then
				// remaining space is split proportionally by natural excess.
				extra := available - totalMinW
				sumExcess := 0
				for i := range colW {
					sumExcess += colW[i] - minColW[i]
				}
				for i := range colW {
					bonus := 0
					if sumExcess > 0 {
						bonus = (colW[i] - minColW[i]) * extra / sumExcess
					}
					colW[i] = minColW[i] + bonus
				}
			} else {
				// Even minimum word widths don't fit — proportional scaling,
				// hard breaks are unavoidable.
				for i := range colW {
					colW[i] = max(1, colW[i]*available/sumColW)
				}
			}
		}
	}

	ac := tviewColor(Theme.Accent)
	bc := tviewColor(Theme.Border)

	alignOf := func(i int) extast.Alignment {
		if i < len(tbl.alignments) {
			return tbl.alignments[i]
		}
		return extast.AlignNone
	}

	// padCell fills one cell line to exactly cw visible columns.
	padCell := func(content string, w, cw int, align extast.Alignment) string {
		extra := cw - w
		if extra < 0 {
			extra = 0
		}
		spaces := strings.Repeat(" ", extra)
		switch align {
		case extast.AlignRight:
			return spaces + content + "[-:-:-]"
		case extast.AlignCenter:
			l := extra / 2
			r := extra - l
			return strings.Repeat(" ", l) + content + "[-:-:-]" + strings.Repeat(" ", r)
		default:
			return content + "[-:-:-]" + spaces
		}
	}

	// emitRow transposes per-cell line-slices into display rows.
	// When cells have different line counts the shorter ones are padded with
	// blank lines so all columns stay aligned.
	emitRow := func(cellLines [][]string) []string {
		maxL := 0
		for _, lines := range cellLines {
			if len(lines) > maxL {
				maxL = len(lines)
			}
		}
		out := make([]string, maxL)
		for li := 0; li < maxL; li++ {
			parts := make([]string, numCols)
			for i := 0; i < numCols; i++ {
				var content string
				var w int
				if i < len(cellLines) && li < len(cellLines[i]) {
					content = cellLines[i][li]
					w = tview.TaggedStringWidth(content)
				}
				parts[i] = " " + padCell(content, w, colW[i], alignOf(i)) + " "
			}
			inner := strings.Join(parts, fmt.Sprintf("[%s]│[-:-:-]", bc))
			out[li] = fmt.Sprintf("[%s]│[-:-:-]%s[%s]│[-:-:-]", bc, inner, bc)
		}
		return out
	}

	hRule := func(left, mid, right string) string {
		var sb strings.Builder
		fmt.Fprintf(&sb, "[%s]%s", bc, left)
		for i, w := range colW {
			if i > 0 {
				sb.WriteString(mid)
			}
			sb.WriteString(strings.Repeat("─", w+2))
		}
		sb.WriteString(right + "[-:-:-]")
		return sb.String()
	}

	// Phase 3: wrap each cell to its column width, emit multi-line rows.
	*out = append(*out, hRule("┌", "┬", "┐"))

	if len(tbl.header) > 0 {
		cellLines := make([][]string, numCols)
		for i := 0; i < numCols; i++ {
			var spans []mdSpan
			if i < len(tbl.header) {
				spans = tbl.header[i]
			}
			plain := spansPlain(spans)
			raw := tview.WordWrap(tview.Escape(plain), colW[i])
			styled := make([]string, len(raw))
			for j, l := range raw {
				styled[j] = fmt.Sprintf("[%s][::b]%s[-:-:-]", ac, l)
			}
			if len(styled) == 0 {
				styled = []string{""}
			}
			cellLines[i] = styled
		}
		*out = append(*out, emitRow(cellLines)...)
		*out = append(*out, hRule("├", "┼", "┤"))
	}

	for _, row := range tbl.rows {
		cellLines := make([][]string, numCols)
		for i := 0; i < numCols; i++ {
			var spans []mdSpan
			if i < len(row) {
				spans = row[i]
			}
			lines := wrapSpans(spans, colW[i])
			if len(lines) == 0 {
				lines = []string{""}
			}
			cellLines[i] = lines
		}
		*out = append(*out, emitRow(cellLines)...)
	}

	*out = append(*out, hRule("└", "┴", "┘"))
}

// ─── public render entry point ────────────────────────────────────────────────

// renderMarkdown parses and immediately renders src to tview-tagged lines.
// Callers that need resize-efficient rendering should use the cached path via
// ChatView.renderMarkdownCached instead.
func renderMarkdown(src string, maxW int) []string {
	return renderBlocks(parseMarkdown(src), maxW)
}

// renderBlocks renders a pre-parsed block list to lines at the given width.
func renderBlocks(blocks []mdBlock, maxW int) []string {
	var out []string
	for _, b := range blocks {
		out = append(out, b.renderLines(maxW)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// ─── inline span collection ───────────────────────────────────────────────────

type mdStyle struct {
	bold          bool
	italic        bool
	code          bool
	strikethrough bool
}

func (s mdStyle) openTag() string {
	if s.code {
		return fmt.Sprintf("[-:-:-][%s]", tviewColor(Theme.CodeColor))
	}
	attrs := ""
	if s.bold {
		attrs += "b"
	}
	if s.italic {
		attrs += "i"
	}
	if s.strikethrough {
		attrs += "s"
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

	case extast.KindStrikethrough:
		s2 := style
		s2.strikethrough = true
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

// spansToTagged renders spans to a single tview-tagged line without wrapping.
func spansToTagged(spans []mdSpan) string {
	sentinel := mdStyle{code: true, bold: true, italic: true, strikethrough: true}
	var sb strings.Builder
	cur := sentinel
	for _, sp := range spans {
		for _, r := range []rune(sp.text) {
			if r == '\n' {
				r = ' '
			}
			if sp.style != cur {
				sb.WriteString(sp.style.openTag())
				cur = sp.style
			}
			if r == '[' {
				sb.WriteString("[[]")
			} else {
				sb.WriteRune(r)
			}
		}
	}
	return sb.String()
}

// ─── span-aware word wrap ─────────────────────────────────────────────────────

type styledRune struct {
	r     rune
	style mdStyle
}

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

	sentinel := mdStyle{code: true, bold: true, italic: true, strikethrough: true}

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
				start = lastSpace + 1
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
