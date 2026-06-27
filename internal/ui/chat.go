package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksana/hypane/internal/session"
)

// selPoint is a (document-line, absolute-screen-column) pair for drag selection.
type selPoint struct {
	docLine int
	screenX int
}

// tviewTagRe matches tview color/attribute tags like [#rrggbb], [-], [::b].
// [^\[] excludes a literal '[' so [[] (escaped bracket) is never matched.
var tviewTagRe = regexp.MustCompile(`\[[^\[]*\]`)

// stripTags removes tview color tags and unescapes [[] → [.
func stripTags(s string) string {
	s = tviewTagRe.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "[[]", "[")
}


// ChatView displays the conversation as individually bordered message boxes.
type ChatView struct {
	*tview.TextView
	messages     []session.Message
	lastWidth    int
	TotalLines   int // read by Scrollbar
	hoverIdx        int // index into renderedMsgs; -1 = none
	selectedIdx     int // box highlighted by last click; -1 = none
	lastSelectedIdx int // selectedIdx at last buildText call; -2 = never built
	renderedMsgs []session.Message
	msgStartLine []int // document line where each renderedMsg's top border starts

	// drag-to-select state
	selAnchor    selPoint
	selCursor    selPoint
	selActive    bool
	dragging     bool
	anchorBox    int // box index where drag started (-1 = none)
	selCursorBox int // last box the cursor touched during drag

	// box bounds per renderedMsg, relative to inner-rect left (ix)
	// boxLeft[i] = leftPad, boxRight[i] = leftPad+boxW
	boxLeft  []int
	boxRight []int

	// mdCache stores parsed markdown blocks keyed by content string so that
	// resize only re-wraps without re-running goldmark.
	mdCache map[string][]mdBlock
}

// NewChatView creates the scrollable message display.
func NewChatView() *ChatView {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(false) // we manage layout manually
	tv.SetBorder(false)   // no outer frame; messages have their own boxes
	tv.SetBackgroundColor(Theme.Background)
	return &ChatView{
		TextView:        tv,
		hoverIdx:        -1,
		selectedIdx:     -1,
		lastSelectedIdx: -2,
		anchorBox:       -1,
		selCursorBox:    -1,
		mdCache:         make(map[string][]mdBlock),
	}
}

// SetFocused is called by focus/blur hooks; no visible border to update.
func (cv *ChatView) SetFocused(_ bool) {}

// Draw rebuilds text when width or hover changes, then renders and draws overlays.
func (cv *ChatView) Draw(screen tcell.Screen) {
	_, _, w, _ := cv.GetInnerRect()
	if w > 0 && (w != cv.lastWidth || cv.selectedIdx != cv.lastSelectedIdx) {
		cv.lastWidth = w
		cv.lastSelectedIdx = cv.selectedIdx
		cv.buildText(w)
	}
	cv.TextView.Draw(screen)
	cv.drawSelectionOverlay(screen)
}

// drawSelectionOverlay dispatches to partial (within-box) or whole-box drawing.
func (cv *ChatView) drawSelectionOverlay(screen tcell.Screen) {
	if !cv.selActive {
		return
	}
	if cv.isWhole() {
		cv.drawWholeSel(screen)
	} else {
		cv.drawPartialSel(screen)
	}
}

// isWhole reports whether the cursor is on a border line or outside the anchor
// box, triggering whole-box-at-a-time selection mode.
// Content lines are strictly inside both borders: (startDoc, endDoc-1).
func (cv *ChatView) isWhole() bool {
	if !cv.selActive || cv.anchorBox < 0 || cv.anchorBox >= len(cv.msgStartLine) {
		return false
	}
	startDoc, endDoc := cv.boxDocRange(cv.anchorBox)
	cl := cv.selCursor.docLine
	return cl <= startDoc || cl >= endDoc-1
}

// boxDocRange returns the [startDoc, endDoc) doc-line range for box i,
// excluding the blank separator line that follows each non-last box.
func (cv *ChatView) boxDocRange(i int) (startDoc, endDoc int) {
	startDoc = cv.msgStartLine[i]
	if i+1 < len(cv.msgStartLine) {
		endDoc = cv.msgStartLine[i+1] - 1
	} else {
		endDoc = cv.TotalLines - 1
	}
	return
}

// drawPartialSel highlights the column range within the anchor box's content
// area (not including top/bottom border lines or the │ border columns).
func (cv *ChatView) drawPartialSel(screen tcell.Screen) {
	if cv.anchorBox < 0 || cv.anchorBox >= len(cv.boxLeft) {
		return
	}
	ix, iy, _, ih := cv.GetInnerRect()
	scrollY, _ := cv.GetScrollOffset()

	anchor, cur := cv.selAnchor, cv.selCursor
	if anchor.docLine > cur.docLine ||
		(anchor.docLine == cur.docLine && anchor.screenX > cur.screenX) {
		anchor, cur = cur, anchor
	}

	lp := cv.boxLeft[cv.anchorBox]
	bw := cv.boxRight[cv.anchorBox] - lp
	contentLeft := ix + lp + 2   // past left │ and its inner space
	contentRight := ix + lp + bw - 2 // before inner space and right │

	boxStart, boxEndExcl := cv.boxDocRange(cv.anchorBox)
	bottomBorder := boxEndExcl - 1

	selStyle := tcell.StyleDefault.
		Foreground(tcell.NewRGBColor(220, 220, 230)).
		Background(tcell.NewRGBColor(50, 80, 150))

	for docLine := anchor.docLine; docLine <= cur.docLine; docLine++ {
		if docLine == boxStart || docLine == bottomBorder {
			continue // skip top and bottom border lines
		}
		sy := iy + (docLine - scrollY)
		if sy < iy || sy >= iy+ih {
			continue
		}
		x0 := contentLeft
		x1 := contentRight
		if docLine == anchor.docLine && anchor.screenX > x0 {
			x0 = anchor.screenX
		}
		if docLine == cur.docLine && cur.screenX < x1 {
			x1 = cur.screenX
		}
		for x := x0; x < x1; x++ {
			r, comb, _, _ := screen.GetContent(x, sy)
			screen.SetContent(x, sy, r, comb, selStyle)
		}
	}
}

// drawWholeSel highlights entire message boxes (including borders) for the
// range of boxes covered by the current selection.
func (cv *ChatView) drawWholeSel(screen tcell.Screen) {
	lo, hi := cv.anchorBox, cv.selCursorBox
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < 0 {
		lo = 0
	}
	if hi < 0 || hi >= len(cv.renderedMsgs) {
		hi = len(cv.renderedMsgs) - 1
	}

	ix, iy, iw, ih := cv.GetInnerRect()
	scrollY, _ := cv.GetScrollOffset()

	selStyle := tcell.StyleDefault.
		Foreground(tcell.NewRGBColor(220, 220, 230)).
		Background(tcell.NewRGBColor(50, 80, 150))

	for msgIdx := lo; msgIdx <= hi; msgIdx++ {
		if msgIdx >= len(cv.boxLeft) {
			break
		}
		startDoc, endDoc := cv.boxDocRange(msgIdx)
		bLeft := ix + cv.boxLeft[msgIdx]
		bRight := ix + cv.boxRight[msgIdx]

		for docLine := startDoc; docLine < endDoc; docLine++ {
			sy := iy + (docLine - scrollY)
			if sy < iy || sy >= iy+ih {
				continue
			}
			for x := bLeft; x < bRight && x < ix+iw; x++ {
				r, comb, _, _ := screen.GetContent(x, sy)
				screen.SetContent(x, sy, r, comb, selStyle)
			}
		}
	}
}


// Render stores the message list and rebuilds the display text.
func (cv *ChatView) Render(messages []session.Message) {
	cv.messages = messages
	_, _, w, _ := cv.GetInnerRect()
	if w <= 0 {
		w = 80
	}
	cv.lastWidth = w
	cv.buildText(w)
	cv.TextView.ScrollToEnd()
}

// HoveredContent returns the raw content of whichever message the mouse is over.
func (cv *ChatView) HoveredContent() string {
	if cv.hoverIdx < 0 || cv.hoverIdx >= len(cv.renderedMsgs) {
		return ""
	}
	return cv.renderedMsgs[cv.hoverIdx].Content
}

// MouseHandler wraps TextView's handler to track hover and drag-to-select.
func (cv *ChatView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	orig := cv.TextView.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		ix, iy, iw, ih := cv.GetInnerRect()
		scrollY, _ := cv.GetScrollOffset()
		mx, my := event.Position()
		docLine := (my - iy) + scrollY

		// Run orig first so scrolling and focus still work.
		consumed := false
		var capture tview.Primitive
		if orig != nil {
			consumed, capture = orig(action, event, setFocus)
		}

		// When the event is outside our rect, clear stale hover and bail.
		// Check both axes: Flex calls all children in order, so without the
		// x-check the chat handler would consume scrollbar-column clicks
		// (findMsgAt uses y only, so it matches messages regardless of x).
		if mx < ix || mx >= ix+iw || my < iy || my >= iy+ih {
			cv.hoverIdx = -1
			return consumed, capture
		}

		switch action {
		case tview.MouseLeftDown:
			cv.hoverIdx = cv.findMsgAt(docLine)
			cv.selActive = false
			if cv.hoverIdx >= 0 {
				cv.selAnchor = selPoint{docLine: docLine, screenX: mx}
				cv.selCursor = cv.selAnchor
				cv.anchorBox = cv.hoverIdx
				cv.selCursorBox = cv.hoverIdx
				cv.dragging = true
				consumed = true
			} else {
				cv.dragging = false
				cv.anchorBox = -1
				cv.selCursorBox = -1
			}

		case tview.MouseMove:
			cv.hoverIdx = cv.findMsgAt(docLine)
			if cv.dragging {
				cv.selCursor = selPoint{docLine: docLine, screenX: mx}
				cv.selActive = cv.selCursor != cv.selAnchor
				cv.updateSelCursorBox(docLine)
				consumed = true
			}

		case tview.MouseLeftUp:
			if cv.dragging {
				cv.selCursor = selPoint{docLine: docLine, screenX: mx}
				cv.dragging = false
				cv.selActive = cv.selCursor != cv.selAnchor
				cv.updateSelCursorBox(docLine)
				consumed = true
			}

		case tview.MouseLeftClick:
			cv.hoverIdx = cv.findMsgAt(docLine)
			cv.selectedIdx = cv.hoverIdx
		}

		return consumed, capture
	}
}

func (cv *ChatView) findMsgAt(docLine int) int {
	for i, start := range cv.msgStartLine {
		end := cv.TotalLines
		if i+1 < len(cv.msgStartLine) {
			end = cv.msgStartLine[i+1]
		}
		if docLine >= start && docLine < end {
			return i
		}
	}
	return -1
}

// updateSelCursorBox tracks which box the drag cursor is targeting.
// Uses boxDocRange (not findMsgAt) so separator lines between boxes are not
// attributed to any box — crossing a separator keeps the previous cursor box,
// meaning a box is only selected once its own border is reached.
func (cv *ChatView) updateSelCursorBox(docLine int) {
	if len(cv.msgStartLine) == 0 {
		return
	}
	for i := range cv.msgStartLine {
		startDoc, endDoc := cv.boxDocRange(i)
		if docLine >= startDoc && docLine < endDoc {
			cv.selCursorBox = i
			return
		}
	}
	// Cursor is above all boxes or below all boxes — snap to edge.
	// In a separator gap between boxes, keep the previous selCursorBox.
	if docLine < cv.msgStartLine[0] {
		cv.selCursorBox = 0
	} else if docLine >= cv.TotalLines {
		cv.selCursorBox = len(cv.renderedMsgs) - 1
	}
}

// ─── text construction ───────────────────────────────────────────────────────

var hyphaeArt = []string{
	`                   /`,
	`             .---'`,
	`            /`,
	`       .---+----.`,
	`      /          \`,
	`     /            '---.`,
	`    +                  \`,
	`     \              .---+---.`,
	`      '----.       /         \`,
	`            \     /           '`,
	`         .---+---+`,
	`        /        \`,
	`       /          '----.`,
	`  .---+                 \`,
	` /     \                 +---.`,
	`/       '---.           /     \`,
	`+              \         +      '`,
	` \              +-------+`,
	`  '----.       /         \`,
	`        \     /           '----.`,
	`         '---+                  \`,
	`              \                  +`,
	`               '----.            |`,
	`                     \           |`,
}

func (cv *ChatView) renderWelcome(b *strings.Builder, width int) {
	_, _, _, viewH := cv.GetInnerRect()
	subtitle := "terminal coding agent"

	totalH := len(hyphaeArt) + 2
	topPad := (viewH - totalH) / 2
	if topPad < 0 {
		topPad = 0
	}

	ac := tviewColor(Theme.Accent)
	mc := tviewColor(Theme.Muted)

	artW := 0
	for _, line := range hyphaeArt {
		if w := len(line); w > artW {
			artW = w
		}
	}

	for i := 0; i < topPad; i++ {
		b.WriteString("\n")
	}

	hPad := (width - artW) / 2
	if hPad < 0 {
		hPad = 0
	}
	pad := strings.Repeat(" ", hPad)

	for _, line := range hyphaeArt {
		b.WriteString(fmt.Sprintf("[%s]%s%s[-]\n", ac, pad, tview.Escape(line)))
	}

	b.WriteString("\n")

	subPad := (width - len(subtitle)) / 2
	if subPad < 0 {
		subPad = 0
	}
	b.WriteString(fmt.Sprintf("[%s]%s%s[-]\n", mc, strings.Repeat(" ", subPad), subtitle))
}

func (cv *ChatView) buildText(width int) {
	// Doc lines shift whenever text is rebuilt, so any live selection is invalid.
	cv.selActive = false
	cv.dragging = false
	cv.anchorBox = -1
	cv.selCursorBox = -1

	maxW := width * 4 / 5
	if maxW < 20 {
		maxW = 20
	}

	hasDisplayable := false
	for _, msg := range cv.messages {
		if msg.Role != session.RoleTool {
			hasDisplayable = true
			break
		}
	}

	if !hasDisplayable {
		var b strings.Builder
		cv.renderWelcome(&b, width)
		cv.renderedMsgs = nil
		cv.msgStartLine = nil
		cv.boxLeft = nil
		cv.boxRight = nil
		text := b.String()
		cv.TotalLines = strings.Count(text, "\n") + 1
		cv.TextView.SetText(text)
		return
	}

	var b strings.Builder
	var renderedMsgs []session.Message
	var msgStartLine []int
	var boxLeft, boxRight []int

	first := true
	for _, msg := range cv.messages {
		if msg.Role == session.RoleTool {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false

		msgStartLine = append(msgStartLine, strings.Count(b.String(), "\n"))
		renderedMsgs = append(renderedMsgs, msg)
		msgIdx := len(renderedMsgs) - 1
		lp, bw := cv.renderMessageBox(&b, msg, width, maxW, msgIdx == cv.selectedIdx)
		boxLeft = append(boxLeft, lp)
		boxRight = append(boxRight, lp+bw)
	}

	cv.renderedMsgs = renderedMsgs
	cv.msgStartLine = msgStartLine
	cv.boxLeft = boxLeft
	cv.boxRight = boxRight

	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

// renderMessageBox writes a single bordered message box into b.
//
// The box width is compact: sized to the actual content, capped at maxW.
// User boxes are flush to the right edge; assistant boxes are flush to the left.
//
// Box anatomy:
//   ┌─ label ──────────┐
//   │ content line     │
//   └──────────────────┘
// renderMessageBox writes a bordered message box into b and returns the box's
// leftPad and boxW so the caller can record geometry without a separate pass.
// It uses cv.mdCache so that resize re-wraps without re-parsing markdown.
func (cv *ChatView) renderMessageBox(b *strings.Builder, msg session.Message, width, maxW int, isHovered bool) (leftPad, boxW int) {
	borderColor := Theme.Border
	if isHovered {
		borderColor = Theme.BorderFocus
	}
	bc := tviewColor(borderColor)

	dash := func(n int) string {
		if n < 0 {
			n = 0
		}
		return strings.Repeat("─", n)
	}

	// boxLine renders one content row padded to fill contentW columns.
	// [-:-:-] before the padding resets any style from the inner content so
	// the trailing space and border character are unaffected.
	mkBoxLine := func(contentW int) func(inner string, vlen int) string {
		return func(inner string, vlen int) string {
			pad := contentW - vlen
			if pad < 0 {
				pad = 0
			}
			return fmt.Sprintf("[%s]│[-] %s[-:-:-]%s [%s]│[-]", bc, inner, strings.Repeat(" ", pad), bc)
		}
	}

	switch msg.Role {
	case session.RoleUser:
		// label overhead: ┌─ you ┐ = 8 visible cols minimum
		maxContentW := maxW - 4
		lines := wordWrap(msg.Content, maxContentW)
		actualW := 0
		for _, l := range lines {
			if n := tview.TaggedStringWidth(l); n > actualW {
				actualW = n
			}
		}
		boxW = max(8, actualW+4)
		contentW := boxW - 4
		leftPad = width - boxW
		if leftPad < 0 {
			leftPad = 0
		}
		p := strings.Repeat(" ", leftPad)
		uc := tviewColor(Theme.UserColor)
		boxLine := mkBoxLine(contentW)

		// ┌─ you ──...──┐  "─ you " = 6 visible cols
		b.WriteString(p + fmt.Sprintf("[%s]┌─ [%s]you [%s]%s┐[-]", bc, uc, bc, dash(boxW-8)) + "\n")
		for _, line := range lines {
			b.WriteString(p + boxLine(tview.Escape(line), tview.TaggedStringWidth(line)) + "\n")
		}
		b.WriteString(p + fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")

	case session.RoleAssistant:
		ac := tviewColor(Theme.ApexColor)
		mc := tviewColor(Theme.Muted)

		if msg.Error != nil {
			ec := tviewColor(Theme.ErrorColor)
			maxContentW := maxW - 4
			lines := wordWrap(msg.Error.Error(), maxContentW)
			actualW := 0
			for _, l := range lines {
				if n := tview.TaggedStringWidth(l); n > actualW {
					actualW = n
				}
			}
			// ┌─ error ┐ = 10 visible cols minimum
			boxW = max(10, actualW+4)
			contentW := boxW - 4
			boxLine := mkBoxLine(contentW)

			b.WriteString(fmt.Sprintf("[%s]┌─ [%s]error [%s]%s┐[-]", bc, ec, bc, dash(boxW-10)) + "\n")
			for _, line := range lines {
				inner := fmt.Sprintf("[%s]%s[-]", ec, tview.Escape(line))
				b.WriteString(boxLine(inner, tview.TaggedStringWidth(line)) + "\n")
			}
			b.WriteString(fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")
			return
		}

		maxContentW := maxW - 4
		blocks, ok := cv.mdCache[msg.Content]
		if !ok {
			blocks = parseMarkdown(msg.Content)
			cv.mdCache[msg.Content] = blocks
		}
		lines := renderBlocks(blocks, maxContentW)
		actualW := 0
		for _, l := range lines {
			if n := tview.TaggedStringWidth(l); n > actualW {
				actualW = n
			}
		}
		for _, tu := range msg.ToolUses {
			if _, vlen := fmtToolUse(tu); vlen > actualW {
				actualW = vlen
			}
		}
		// ┌─ apex ┐ = 9 cols, ┌─ apex … ┐ = 11 cols
		minBoxW := 9
		if msg.Partial {
			minBoxW = 11
		}
		boxW = max(minBoxW, actualW+4)
		contentW := boxW - 4
		boxLine := mkBoxLine(contentW)

		partialFrag := ""
		extraW := 0
		if msg.Partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", mc)
			extraW = 2
		}
		// ┌─ apex ──...──┐  "─ apex " = 7 visible cols
		b.WriteString(fmt.Sprintf("[%s]┌─ [%s]apex [%s]%s%s┐[-]",
			bc, ac, bc, partialFrag, dash(boxW-9-extraW)) + "\n")
		for _, line := range lines {
			b.WriteString(boxLine(line, tview.TaggedStringWidth(line)) + "\n")
		}
		for _, tu := range msg.ToolUses {
			inner, vlen := fmtToolUse(tu)
			b.WriteString(boxLine(inner, vlen) + "\n")
		}
		b.WriteString(fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")
	}
	return
}

// fmtToolUse returns the colored inline string and its visible terminal column width.
func fmtToolUse(tu session.ToolUse) (string, int) {
	arg := formatInput(tu.Input)
	toolC := tviewColor(Theme.ToolColor)
	mutedC := tviewColor(Theme.Muted)

	var s string
	switch tu.State {
	case "running":
		s = fmt.Sprintf("[%s]▶ %s[-][%s]%s[-] [%s]…[-]",
			toolC, tview.Escape(tu.Name), mutedC, arg, mutedC)
	case "done":
		s = fmt.Sprintf("[%s]▶ %s[-][%s]%s[-] [%s]✓[-]",
			toolC, tview.Escape(tu.Name), mutedC, arg, tviewColor(Theme.SuccessColor))
	case "error":
		s = fmt.Sprintf("[%s]▶ %s[-][%s]%s[-] [%s]✗ %s[-]",
			toolC, tview.Escape(tu.Name), mutedC, arg, tviewColor(Theme.ErrorColor), tview.Escape(tu.Output))
	default:
		s = fmt.Sprintf("[%s]▷ %s[-][%s]%s[-]",
			toolC, tview.Escape(tu.Name), mutedC, arg)
	}
	return s, visibleLen(s)
}

// ─── selection ───────────────────────────────────────────────────────────────

// HasSelection reports whether there is an active drag selection.
func (cv *ChatView) HasSelection() bool { return cv.selActive }

// ClearHover clears the hover highlight; called via SetMouseCapture when the
// mouse moves outside the chat view's rect (which the chat handler never sees).
func (cv *ChatView) ClearHover() {
	cv.hoverIdx = -1
	cv.selectedIdx = -1
}

func (cv *ChatView) ClearSelection() {
	cv.selActive = false
	cv.dragging = false
	cv.anchorBox = -1
	cv.selCursorBox = -1
}

// SelectedText returns the plain text covered by the current drag selection.
// In partial mode (cursor inside anchor box) it extracts the column range.
// In whole-box mode it returns full box content, with role prefixes when
// multiple boxes are selected.
func (cv *ChatView) SelectedText() string {
	if !cv.selActive {
		return ""
	}
	if cv.isWhole() {
		return cv.selectedTextWhole()
	}
	return cv.selectedTextPartial()
}

func (cv *ChatView) selectedTextPartial() string {
	ix, _, _, _ := cv.GetInnerRect()

	anchor, cur := cv.selAnchor, cv.selCursor
	if anchor.docLine > cur.docLine ||
		(anchor.docLine == cur.docLine && anchor.screenX > cur.screenX) {
		anchor, cur = cur, anchor
	}

	maxW := cv.lastWidth * 4 / 5
	if maxW < 20 {
		maxW = 20
	}

	var parts []string
	lastMsgIdx := -2
	var allLines []string
	var leftPad int

	for docLine := anchor.docLine; docLine <= cur.docLine; docLine++ {
		msgIdx := cv.findMsgAt(docLine)
		if msgIdx < 0 || msgIdx >= len(cv.renderedMsgs) {
			continue
		}
		if msgIdx != lastMsgIdx {
			lastMsgIdx = msgIdx
			allLines, leftPad = computeMsgContent(cv.renderedMsgs[msgIdx], cv.lastWidth, maxW)
		}

		// line 0 within box = top border, lines 1..N = content, line N+1 = bottom border.
		lineWithinBox := docLine - cv.msgStartLine[msgIdx] - 1
		if lineWithinBox < 0 || lineWithinBox >= len(allLines) {
			continue
		}

		contentLine := allLines[lineWithinBox]
		runes := []rune(contentLine)

		// Content text starts at ix + leftPad + 2 (past "│ ").
		contentStartX := ix + leftPad + 2
		from, to := 0, len(runes)
		if docLine == anchor.docLine {
			if f := anchor.screenX - contentStartX; f > from {
				from = f
			}
		}
		if docLine == cur.docLine {
			if t := cur.screenX - contentStartX; t < to {
				to = t
			}
		}
		if from < 0 {
			from = 0
		}
		if to < 0 {
			to = 0
		}
		if from > len(runes) {
			from = len(runes)
		}
		if to > len(runes) {
			to = len(runes)
		}
		if from > to {
			to = from
		}
		parts = append(parts, string(runes[from:to]))
	}

	return strings.Join(parts, "\n")
}

func (cv *ChatView) selectedTextWhole() string {
	lo, hi := cv.anchorBox, cv.selCursorBox
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < 0 {
		lo = 0
	}
	if hi >= len(cv.renderedMsgs) {
		hi = len(cv.renderedMsgs) - 1
	}

	maxW := cv.lastWidth * 4 / 5
	if maxW < 20 {
		maxW = 20
	}

	multi := hi > lo
	var parts []string
	for i := lo; i <= hi; i++ {
		msg := cv.renderedMsgs[i]
		lines, _ := computeMsgContent(msg, cv.lastWidth, maxW)
		content := strings.Join(lines, "\n")
		if multi {
			role := "assistant"
			if msg.Role == session.RoleUser {
				role = "you"
			}
			parts = append(parts, role+":\n"+content)
		} else {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

// computeMsgContent returns the plain-text content lines for a message and the
// screen-column left padding of its box (non-zero only for right-aligned user boxes).
func computeMsgContent(msg session.Message, width, maxW int) (allLines []string, leftPad int) {
	maxContentW := maxW - 4
	switch msg.Role {
	case session.RoleUser:
		lines := wordWrap(msg.Content, maxContentW)
		actualW := 0
		for _, l := range lines {
			if n := tview.TaggedStringWidth(l); n > actualW {
				actualW = n
			}
		}
		boxW := max(8, actualW+4)
		lp := width - boxW
		if lp < 0 {
			lp = 0
		}
		return lines, lp
	case session.RoleAssistant:
		if msg.Error != nil {
			return wordWrap(msg.Error.Error(), maxContentW), 0
		}
		raw := renderMarkdown(msg.Content, maxContentW)
		all := make([]string, len(raw))
		for i, l := range raw {
			all[i] = stripTags(l)
		}
		for _, tu := range msg.ToolUses {
			inner, _ := fmtToolUse(tu)
			all = append(all, stripTags(inner))
		}
		return all, 0
	}
	return nil, 0
}


// ─── text helpers ─────────────────────────────────────────────────────────────

// wordWrap splits text into lines of at most maxW columns.
// Breaks on spaces and CJK character boundaries; hard-breaks overlong tokens.
func wordWrap(text string, maxW int) []string {
	if maxW <= 0 {
		maxW = 40
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		out = append(out, wrapParagraph(para, maxW)...)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// isCJKRune reports whether r is a CJK ideograph or similar character that
// may be broken at any position (each rune is its own break opportunity).
func isCJKRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x11FF) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0x2FFF) || // CJK Radicals, Kangxi
		(r >= 0x3000 && r <= 0x9FFF) || // CJK Unified + kana + misc
		(r >= 0xA000 && r <= 0xA4CF) || // Yi
		(r >= 0xAC00 && r <= 0xD7AF) || // Hangul Syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK Compatibility Ideographs
		(r >= 0xFE30 && r <= 0xFE4F) || // CJK Compatibility Forms
		(r >= 0xFF00 && r <= 0xFFEF) || // Halfwidth/Fullwidth Forms
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0x2A700 && r <= 0x2CEAF) || // CJK Extensions C/D/E
		(r >= 0x2CEB0 && r <= 0x2EBEF) // CJK Extension F
}

func wrapParagraph(para string, maxW int) []string {
	if tview.TaggedStringWidth(para) <= maxW {
		return []string{para}
	}

	runes := []rune(para)
	var out []string

	lineStart := 0  // rune index where current line begins
	lineW := 0      // column width of current line
	lastSpace := -1 // rune index of last space break opportunity

	flush := func(end int, nextStart int) {
		out = append(out, string(runes[lineStart:end]))
		lineStart = nextStart
		lineW = 0
		lastSpace = -1
	}

	for i, r := range runes {
		rW := tview.TaggedStringWidth(string(r))
		cjk := isCJKRune(r)

		// CJK: break before this rune if it would overflow.
		if cjk && lineW+rW > maxW && lineW > 0 {
			flush(i, i)
		}

		if r == ' ' {
			lastSpace = i
		}

		lineW += rW

		// After adding this rune, did we overflow?
		if lineW > maxW {
			if lastSpace > lineStart {
				// Break at the last space; skip the space itself.
				flush(lastSpace, lastSpace+1)
				// Recompute lineW for the chars after the space up to and including i.
				lineW = tview.TaggedStringWidth(string(runes[lineStart : i+1]))
			} else {
				// No break opportunity — hard-break before current rune.
				flush(i, i)
				lineW = rW
			}
		}
	}

	if lineStart < len(runes) {
		out = append(out, string(runes[lineStart:]))
	}
	return out
}

// visibleLen returns the terminal column width of s as tview would render it.
// It strips tview color tags and uses the same uniseg-based measurement tview uses
// internally, so wide characters (emoji, CJK, etc.) are counted correctly.
func visibleLen(s string) int {
	return tview.TaggedStringWidth(s)
}

func formatInput(input string) string {
	if input == "" || input == "{}" {
		return "()"
	}
	input = strings.TrimSpace(input)
	runes := []rune(input)
	if len(runes) > 50 {
		input = string(runes[:47]) + "..."
	}
	return "(" + input + ")"
}
