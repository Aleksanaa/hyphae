package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
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

// renderedLine pairs a tview-tagged display string with a per-visible-column
// copyability mask. copyMask[col] == false means the character at that column
// is a non-copyable format/border element. len(copyMask) equals the visible
// width of text. A nil mask means every column is copyable.
// softWrap true means the line break after this line is a word-wrap artefact,
// not a real newline in the source; adjacent selected lines should be joined
// with a space rather than \n.
type renderedLine struct {
	text     string
	copyMask []bool
	softWrap bool
	// tabular marks a line belonging to a rendered table. Drag-selection reads it
	// to offer spreadsheet-style block selection when a drag spans multiple table
	// rows and columns (see iterPartialSel).
	tabular bool
}

// allCopyMask returns a mask of length n with every position marked copyable.
func allCopyMask(n int) []bool {
	m := make([]bool, n)
	for i := range m {
		m[i] = true
	}
	return m
}

// mdBlock is a parsed, width-independent markdown block. renderLines re-wraps
// it to the given visible column width — called on every resize, never re-parses.
// Each renderedLine carries an explicit per-column copyability mask produced at
// generation time; no post-hoc character detection is needed.
type mdBlock interface {
	renderLines(maxW int) []renderedLine
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

func (b *paragraphBlock) renderLines(maxW int) []renderedLine {
	rls := wrapSpans(b.spans, maxW)
	for i := range rls {
		rls[i].copyMask = allCopyMask(tview.TaggedStringWidth(rls[i].text))
	}
	return rls
}

func (b *headingBlock) renderLines(maxW int) []renderedLine {
	prefix := strings.Repeat("#", b.level) + " "
	plain := spansPlain(b.spans)
	ac := TC.Accent
	wrapped := tview.WordWrap(tview.Escape(prefix+plain), maxW)
	out := make([]renderedLine, len(wrapped))
	for i, l := range wrapped {
		text := fmt.Sprintf("[%s][::b]%s[-:-:-]", ac, l)
		out[i] = renderedLine{
			text:     text,
			copyMask: allCopyMask(tview.TaggedStringWidth(text)),
			softWrap: i < len(wrapped)-1,
		}
	}
	return out
}

func (b *codeBlock) renderLines(maxW int) []renderedLine {
	bc := TC.Border
	mc := TC.Muted
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

	var inner []renderedLine
	if b.lang != "" {
		inner = b.renderHighlighted(innerW)
	} else {
		inner = b.renderPlain(innerW)
	}

	out := make([]renderedLine, 0, len(inner)+2)

	// Top border: entirely non-copyable (mask is all-false = zero value).
	top := topBorder()
	out = append(out, renderedLine{text: top, copyMask: make([]bool, tview.TaggedStringWidth(top))})

	for _, rl := range inner {
		visW := tview.TaggedStringWidth(rl.text)
		pad := max(0, innerW-visW)
		text := fmt.Sprintf("[%s]│[-:-:-] %s%s [%s]│[-:-:-]", bc, rl.text, strings.Repeat(" ", pad), bc)
		// Visible layout: │(1) space(1) content(visW) padding(pad) space(1) │(1)
		// Only the content columns are copyable.
		totalW := 2 + visW + pad + 2
		mask := make([]bool, totalW)
		for col := 2; col < 2+visW; col++ {
			mask[col] = true
		}
		out = append(out, renderedLine{text: text, copyMask: mask, softWrap: rl.softWrap})
	}

	// Bottom border: entirely non-copyable.
	bot := botBorder
	out = append(out, renderedLine{text: bot, copyMask: make([]bool, tview.TaggedStringWidth(bot))})
	return out
}

func (b *codeBlock) renderPlain(innerW int) []renderedLine {
	cc := TC.CodeColor
	var out []renderedLine
	for _, line := range b.lines {
		// Measure after escaping: raw code may contain "[word]" sequences that
		// tview's tag parser treats as zero-width style tags, so
		// TaggedStringWidth(line) would undercount and skip needed wrapping.
		escaped := tview.Escape(line)
		if innerW <= 0 || tview.TaggedStringWidth(escaped) <= innerW {
			out = append(out, renderedLine{text: fmt.Sprintf("[%s]%s[-:-:-]", cc, escaped)})
			continue
		}
		// Wrap by iterating runes of the unescaped line; individual runes are
		// safe to measure (a lone '[' never forms a complete tag).
		runes := []rune(line)
		start, lineW := 0, 0
		var sub []string
		for i, r := range runes {
			rW := tview.TaggedStringWidth(string(r))
			if lineW+rW > innerW {
				sub = append(sub, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(string(runes[start:i]))))
				start, lineW = i, 0
			}
			lineW += rW
		}
		if start < len(runes) {
			sub = append(sub, fmt.Sprintf("[%s]%s[-:-:-]", cc, tview.Escape(string(runes[start:]))))
		}
		for i, s := range sub {
			out = append(out, renderedLine{text: s, softWrap: i < len(sub)-1})
		}
	}
	if len(out) == 0 {
		out = []renderedLine{{text: ""}}
	}
	return out
}

// renderHighlighted uses chroma to produce per-token colored tview lines.
func (b *codeBlock) renderHighlighted(maxW int) []renderedLine {
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

	var out []renderedLine
	for _, lt := range logicalLines {
		sub := wrapStyledRunesToTagged(lt, maxW)
		for i, s := range sub {
			out = append(out, renderedLine{text: s, softWrap: i < len(sub)-1})
		}
	}
	if len(out) == 0 {
		out = []renderedLine{{text: ""}}
	}
	return out
}

// styledRunesToTagged converts diffStyledRune slices to a tview-tagged string.
func styledRunesToTagged(runes []diffStyledRune) string {
	if len(runes) == 0 {
		return ""
	}
	var sb strings.Builder
	var text strings.Builder
	first := true
	var cur tcell.Color
	for _, sr := range runes {
		fg, _, _ := sr.Style.Decompose()
		if first || fg != cur {
			if text.Len() > 0 {
				sb.WriteString(tview.Escape(text.String()))
				text.Reset()
			}
			fmt.Fprintf(&sb, "[%s]", fg.CSS())
			cur = fg
			first = false
		}
		text.WriteRune(sr.R)
	}
	if text.Len() > 0 {
		sb.WriteString(tview.Escape(text.String()))
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

func (b *thematicBreakBlock) renderLines(maxW int) []renderedLine {
	mc := TC.Muted
	text := fmt.Sprintf("[%s]%s[-:-:-]", mc, strings.Repeat("─", maxW))
	// Entirely non-copyable: mask is all-false.
	return []renderedLine{{text: text, copyMask: make([]bool, maxW)}}
}

func (b *blockquoteBlock) renderLines(maxW int) []renderedLine {
	mc := TC.Muted
	innerW := maxW - 2
	if innerW < 1 {
		innerW = 1
	}
	var inner []renderedLine
	for _, item := range b.items {
		inner = append(inner, item.renderLines(innerW)...)
	}
	out := make([]renderedLine, len(inner))
	for i, rl := range inner {
		text := fmt.Sprintf(" [%s]▎[-:-:-]%s", mc, rl.text)
		// Prepend 2 non-copyable columns (space and ▎), then shift inner mask.
		mask := make([]bool, 2+len(rl.copyMask))
		copy(mask[2:], rl.copyMask)
		out[i] = renderedLine{text: text, copyMask: mask, softWrap: rl.softWrap}
	}
	return out
}

func (b *listBlock) renderLines(maxW int) []renderedLine {
	num := b.start
	var out []renderedLine
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

		var itemLines []renderedLine
		for _, block := range item.blocks {
			itemLines = append(itemLines, block.renderLines(innerW)...)
		}
		for i, rl := range itemLines {
			var prefix string
			if i == 0 {
				prefix = bullet
			} else {
				prefix = cont
			}
			text := prefix + rl.text
			// Bullet/continuation columns are copyable; inner mask follows.
			mask := make([]bool, bulletW+len(rl.copyMask))
			for j := 0; j < bulletW; j++ {
				mask[j] = true
			}
			copy(mask[bulletW:], rl.copyMask)
			out = append(out, renderedLine{text: text, copyMask: mask, softWrap: rl.softWrap})
		}
	}
	return out
}

func (b *tableBlock) renderLines(maxW int) []renderedLine {
	var out []renderedLine
	renderTableBlock(b, maxW, &out)
	for i := range out {
		out[i].tabular = true
	}
	return out
}

func (b *groupBlock) renderLines(maxW int) []renderedLine {
	var out []renderedLine
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
		// Expand tabs to spaces: tview measures a tab as zero width but draws it
		// advancing to the next tab stop, so tab-indented lines would wrap/pad as
		// if narrower than they render and spill past the code block's border.
		line := strings.TrimRight(string(seg.Value(source)), "\n")
		lines[i] = strings.ReplaceAll(line, "\t", strings.Repeat(" ", tview.TabSize))
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

func renderTableBlock(tbl *tableBlock, maxW int, out *[]renderedLine) {
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

	ac := TC.Accent
	bc := TC.Border

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

	// emitRow transposes per-cell line-slices into display rows with masks.
	// Each row's mask marks │ separators and padding as non-copyable; only
	// actual cell content columns are marked true.
	emitRow := func(cellLines [][]string) []renderedLine {
		maxL := 0
		for _, lines := range cellLines {
			if len(lines) > maxL {
				maxL = len(lines)
			}
		}
		result := make([]renderedLine, maxL)
		for li := 0; li < maxL; li++ {
			widths := make([]int, numCols)
			parts := make([]string, numCols)
			for i := 0; i < numCols; i++ {
				var content string
				var w int
				if i < len(cellLines) && li < len(cellLines[i]) {
					content = cellLines[i][li]
					w = tview.TaggedStringWidth(content)
				}
				widths[i] = w
				parts[i] = " " + padCell(content, w, colW[i], alignOf(i)) + " "
			}
			inner := strings.Join(parts, fmt.Sprintf("[%s]│[-:-:-]", bc))
			text := fmt.Sprintf("[%s]│[-:-:-]%s[%s]│[-:-:-]", bc, inner, bc)

			// Build copyability mask: opening │, then per-column regions.
			// Each column: space(F) + content/padding + space(F) + │(F)
			mask := []bool{false} // opening │
			for i := 0; i < numCols; i++ {
				w := widths[i]
				cw := colW[i]
				extra := cw - w
				if extra < 0 {
					extra = 0
				}
				mask = append(mask, false) // space before cell
				switch alignOf(i) {
				case extast.AlignRight:
					for j := 0; j < extra; j++ {
						mask = append(mask, false) // left padding
					}
					for j := 0; j < w; j++ {
						mask = append(mask, true) // content
					}
				case extast.AlignCenter:
					l, r := extra/2, extra-extra/2
					for j := 0; j < l; j++ {
						mask = append(mask, false)
					}
					for j := 0; j < w; j++ {
						mask = append(mask, true) // content
					}
					for j := 0; j < r; j++ {
						mask = append(mask, false)
					}
				default: // AlignLeft / AlignNone
					for j := 0; j < w; j++ {
						mask = append(mask, true) // content
					}
					for j := 0; j < extra; j++ {
						mask = append(mask, false) // right padding
					}
				}
				mask = append(mask, false, false) // space after + │ separator/closer
			}

			result[li] = renderedLine{text: text, copyMask: mask, softWrap: li < maxL-1}
		}
		return result
	}

	hRule := func(left, mid, right string) renderedLine {
		var sb strings.Builder
		fmt.Fprintf(&sb, "[%s]%s", bc, left)
		for i, w := range colW {
			if i > 0 {
				sb.WriteString(mid)
			}
			sb.WriteString(strings.Repeat("─", w+2))
		}
		sb.WriteString(right + "[-:-:-]")
		text := sb.String()
		// Entirely non-copyable.
		return renderedLine{text: text, copyMask: make([]bool, tview.TaggedStringWidth(text))}
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
			rls := wrapSpans(spans, colW[i])
			lines := make([]string, len(rls))
			for j, rl := range rls {
				lines[j] = rl.text
			}
			if len(lines) == 0 {
				lines = []string{""}
			}
			cellLines[i] = lines
		}
		*out = append(*out, emitRow(cellLines)...)
	}

	*out = append(*out, hRule("└", "┴", "┘"))
}

// ─── public render entry points ───────────────────────────────────────────────

// renderBlocksAnnotated renders blocks to lines with per-column copy masks.
func renderBlocksAnnotated(blocks []mdBlock, maxW int) []renderedLine {
	var out []renderedLine
	for _, b := range blocks {
		out = append(out, b.renderLines(maxW)...)
	}
	if len(out) == 0 {
		out = []renderedLine{{text: ""}}
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
		return fmt.Sprintf("[-:-:-][%s]", TC.CodeColor)
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
		if sp.style != cur {
			sb.WriteString(sp.style.openTag())
			cur = sp.style
		}
		sb.WriteString(tview.Escape(strings.ReplaceAll(sp.text, "\n", " ")))
	}
	return sb.String()
}

// ─── span-aware word wrap ─────────────────────────────────────────────────────

type styledRune struct {
	r     rune
	style mdStyle
}

func wrapSpans(spans []mdSpan, maxW int) []renderedLine {
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
		return []renderedLine{{text: ""}}
	}

	sentinel := mdStyle{code: true, bold: true, italic: true, strikethrough: true}

	emitLine := func(from, to int) string {
		if to <= from {
			return ""
		}
		var sb strings.Builder
		var text strings.Builder
		cur := sentinel
		for i := from; i < to; i++ {
			if chars[i].style != cur {
				if text.Len() > 0 {
					sb.WriteString(tview.Escape(text.String()))
					text.Reset()
				}
				sb.WriteString(chars[i].style.openTag())
				cur = chars[i].style
			}
			text.WriteRune(chars[i].r)
		}
		if text.Len() > 0 {
			sb.WriteString(tview.Escape(text.String()))
		}
		return sb.String()
	}

	var lines []renderedLine
	start := 0
	lineW := 0
	lastSpace := -1

	for i, ch := range chars {
		if ch.r == '\n' {
			lines = append(lines, renderedLine{text: emitLine(start, i), softWrap: false})
			start = i + 1
			lineW = 0
			lastSpace = -1
			continue
		}

		rW := uniseg.StringWidth(string(ch.r))

		if rW >= 2 && lineW > 0 && lineW+rW > maxW {
			lines = append(lines, renderedLine{text: emitLine(start, i), softWrap: true})
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
				lines = append(lines, renderedLine{text: emitLine(start, lastSpace), softWrap: true})
				start = lastSpace + 1
				lineW = 0
				for j := start; j <= i; j++ {
					lineW += uniseg.StringWidth(string(chars[j].r))
				}
				lastSpace = -1
			} else {
				lines = append(lines, renderedLine{text: emitLine(start, i), softWrap: true})
				start = i
				lineW = rW
				lastSpace = -1
			}
		}
	}

	lines = append(lines, renderedLine{text: emitLine(start, len(chars)), softWrap: false})

	if len(lines) == 0 {
		lines = []renderedLine{{text: ""}}
	}
	return lines
}
