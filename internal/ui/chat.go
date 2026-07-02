package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/session"
)

// selPoint is a (document-line, absolute-screen-column) pair for drag selection.
type selPoint struct {
	docLine int
	screenX int
}

// stripTags removes tview color/attribute tags and restores escaped brackets.
// tview.Escape converts [x] → [x[] so inner content containing '[' marks an
// escape: the text before the '[' plus a literal ']' is the original sequence.
func stripTags(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		idx := strings.IndexByte(s, '[')
		if idx < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:idx])
		s = s[idx:]
		end := strings.IndexByte(s[1:], ']')
		if end < 0 {
			out.WriteByte('[')
			s = s[1:]
			continue
		}
		inner := s[1 : end+1]
		if i := strings.LastIndexByte(inner, '['); i >= 0 {
			// Escaped bracket sequence: emit the text before '[' wrapped in [ ].
			out.WriteByte('[')
			out.WriteString(inner[:i])
			out.WriteByte(']')
		}
		// Otherwise it's a color/attribute tag — consume silently.
		s = s[end+2:]
	}
	return out.String()
}

// ChatView displays the conversation as individually bordered message boxes.
type ChatView struct {
	*tview.TextView
	messages        []session.Message
	lastWidth       int
	TotalLines      int // read by Scrollbar
	hoverIdx        int // index into renderedMsgs; -1 = none
	selectedIdx     int // box highlighted by last click; -1 = none
	lastSelectedIdx int // selectedIdx at last buildText call; -2 = never built
	// Agent activity rendered after the last message.
	// currentStatus is ephemeral UI-only state for retry countdown; overwritten each update.
	currentStatus      string
	lastCurrentStatus  string
	renderedMsgs       []session.Message
	renderedMsgSessIdx []int // session-message index for each renderedMsg entry
	renderedMsgToolIdx []int // tool-use index within the session message (-1 if not a tool item)
	msgStartLine       []int // document line where each renderedMsg's top border starts

	// callbacks for double-clicking expandable items
	onStatusExpand    func(sessionIdx int)
	onToolGroupExpand func(sessionIdx int)

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

	// copyColMask maps doc-line → per-visible-column copyability mask, populated
	// in buildText from renderBlocksAnnotated. Absent key = fully copyable.
	// Present key: false columns are format/border chars, true are content.
	copyColMask map[int][]bool
	// softWrapLine marks doc-lines whose trailing newline is a word-wrap artefact.
	// When two consecutively-selected lines straddle one of these, they are joined
	// with a space rather than \n so the reconstructed text reads as one paragraph.
	softWrapLine map[int]bool
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

// UpdateCurrentStatus sets the ephemeral status line (connecting, preparing).
// Each call replaces the previous; empty string hides it.
func (cv *ChatView) UpdateCurrentStatus(text string) { cv.currentStatus = text }

// SetStatusExpandCallback registers a function called when the user double-clicks
// a RoleStatus message. The argument is the session-message index.
func (cv *ChatView) SetStatusExpandCallback(fn func(sessionIdx int)) { cv.onStatusExpand = fn }

// SetToolGroupExpandCallback registers a function called when the user double-clicks
// a tool-group summary or an expanded tool box, toggling the whole group.
func (cv *ChatView) SetToolGroupExpandCallback(fn func(sessionIdx int)) { cv.onToolGroupExpand = fn }

// SetFocused is called by focus/blur hooks; no visible border to update.
func (cv *ChatView) SetFocused(_ bool) {}

// Draw rebuilds text when width, selection, or ephemeral status changes.
// Activity items from the session are updated via Render; no extra check needed.
func (cv *ChatView) Draw(screen tcell.Screen) {
	_, _, w, _ := cv.GetInnerRect()
	if w > 0 && (w != cv.lastWidth || cv.selectedIdx != cv.lastSelectedIdx || cv.currentStatus != cv.lastCurrentStatus) {
		cv.lastWidth = w
		cv.lastSelectedIdx = cv.selectedIdx
		cv.lastCurrentStatus = cv.currentStatus
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

// iterPartialSel calls fn for each content doc-line in the current partial
// selection. fn receives content-relative column indices (colLo, colHi) and
// the copyColMask for that line (nil = fully copyable). Box border lines are
// skipped; anchor/cursor are normalised internally.
func (cv *ChatView) iterPartialSel(fn func(docLine, colLo, colHi int, mask []bool)) {
	if cv.anchorBox < 0 || cv.anchorBox >= len(cv.boxLeft) {
		return
	}
	ix, _, _, _ := cv.GetInnerRect()
	lp := cv.boxLeft[cv.anchorBox]
	bw := cv.boxRight[cv.anchorBox] - lp
	contentLeft := ix + lp + 2
	contentRight := ix + lp + bw - 2

	anchor, cur := cv.selAnchor, cv.selCursor
	if anchor.docLine > cur.docLine ||
		(anchor.docLine == cur.docLine && anchor.screenX > cur.screenX) {
		anchor, cur = cur, anchor
	}

	boxStart, boxEndExcl := cv.boxDocRange(cv.anchorBox)
	bottomBorder := boxEndExcl - 1

	for docLine := anchor.docLine; docLine <= cur.docLine; docLine++ {
		if docLine == boxStart || docLine == bottomBorder {
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
		fn(docLine, x0-contentLeft, x1-contentLeft, cv.copyColMask[docLine])
	}
}

// drawPartialSel highlights copyable columns within the anchor box's content
// area (not including top/bottom border lines or the │ border columns).
func (cv *ChatView) drawPartialSel(screen tcell.Screen) {
	if cv.anchorBox < 0 || cv.anchorBox >= len(cv.boxLeft) {
		return
	}
	ix, iy, _, ih := cv.GetInnerRect()
	lp := cv.boxLeft[cv.anchorBox]
	contentLeft := ix + lp + 2
	scrollY, _ := cv.GetScrollOffset()

	selStyle := tcell.StyleDefault.
		Foreground(tcell.NewRGBColor(220, 220, 230)).
		Background(tcell.NewRGBColor(50, 80, 150))

	cv.iterPartialSel(func(docLine, colLo, colHi int, mask []bool) {
		sy := iy + (docLine - scrollY)
		if sy < iy || sy >= iy+ih {
			return
		}
		for col := colLo; col < colHi; col++ {
			if mask != nil && (col >= len(mask) || !mask[col]) {
				continue // non-copyable: format char or trailing blank
			}
			x := contentLeft + col
			r, comb, _, _ := screen.GetContent(x, sy)
			screen.SetContent(x, sy, r, comb, selStyle)
		}
	})
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

// Render updates messages from the session snapshot and rebuilds the display text.
// Auto-scrolls to the end only when the view was already at the bottom; otherwise
// restores the previous scroll position so the user can read earlier content.
func (cv *ChatView) Render(messages []session.Message) {
	cv.messages = messages
	_, _, w, h := cv.GetInnerRect()
	if w <= 0 {
		w = 80
	}
	scrollY, _ := cv.GetScrollOffset()
	atBottom := h <= 0 || scrollY+h >= cv.TotalLines
	cv.lastWidth = w
	cv.lastCurrentStatus = cv.currentStatus
	cv.buildText(w)
	if atBottom {
		cv.TextView.ScrollToEnd()
	} else {
		cv.TextView.ScrollTo(scrollY, 0)
	}
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
		_, iy, _, _ := cv.GetInnerRect()
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
		if !cv.InInnerRect(mx, my) {
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

		case tview.MouseLeftDoubleClick:
			idx := cv.findMsgAt(docLine)
			if idx < 0 || idx >= len(cv.renderedMsgs) {
				break
			}
			m := cv.renderedMsgs[idx]
			sessIdx := cv.renderedMsgSessIdx[idx]
			toolIdx := cv.renderedMsgToolIdx[idx]
			if (toolIdx >= 0 || toolIdx == -2) && cv.onToolGroupExpand != nil {
				// Tool group summary (toolIdx==-2) or expanded tool box (toolIdx>=0): toggle group.
				cv.onToolGroupExpand(sessIdx)
			} else if ((m.Role == session.RoleStatus && m.Content != "") || m.ExpandedBox) && cv.onStatusExpand != nil {
				cv.onStatusExpand(sessIdx)
			}
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

	ac := TC.Accent
	mc := TC.Muted

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
		cv.renderedMsgSessIdx = nil
		cv.renderedMsgToolIdx = nil
		cv.msgStartLine = nil
		cv.boxLeft = nil
		cv.boxRight = nil
		cv.appendRetryCountdown(&b)
		text := b.String()
		cv.TotalLines = strings.Count(text, "\n") + 1
		cv.TextView.SetText(text)
		return
	}

	var b strings.Builder
	var renderedMsgs []session.Message
	var renderedMsgSessIdx []int
	var renderedMsgToolIdx []int
	var msgStartLine []int
	var boxLeft, boxRight []int

	first := true
	lineCount := 0

	addEntry := func(entry session.Message, sessIdx, toolIdx int, lp, bw, lines int) {
		renderedMsgs = append(renderedMsgs, entry)
		renderedMsgSessIdx = append(renderedMsgSessIdx, sessIdx)
		renderedMsgToolIdx = append(renderedMsgToolIdx, toolIdx)
		msgStartLine = append(msgStartLine, lineCount)
		boxLeft = append(boxLeft, lp)
		boxRight = append(boxRight, lp+bw)
		lineCount += lines
	}
	for i, msg := range cv.messages {
		if msg.Role == session.RoleTool {
			continue
		}
		if msg.Role == session.RoleStatus {
			if msg.Content == "" {
				continue
			}
			if !first {
				b.WriteString("\n")
				lineCount++
			}
			first = false
			prevLen := b.Len()
			if msg.ThinkingExpanded {
				thinking, partial := "", false
				for j := i + 1; j < len(cv.messages); j++ {
					if cv.messages[j].Role == session.RoleAssistant {
						thinking = cv.messages[j].Thinking
						partial = cv.messages[j].Partial
						break
					}
				}
				entry := session.Message{
					Role:        session.RoleAssistant,
					Content:     thinking,
					Partial:     partial,
					ExpandedBox: true,
					BoxTitle:    fmt.Sprintf("[%s]apex[-][%s] (thoughts)[-]", TC.ApexColor, TC.Muted),
				}
				eIdx := len(renderedMsgs) // index before append
				lp, bw := cv.renderMessageBox(&b, entry, width, maxW, eIdx == cv.selectedIdx)
				addEntry(entry, i, -1, lp, bw, strings.Count(b.String()[prevLen:], "\n"))
			} else {
				b.WriteString("  ")
				b.WriteString(msg.Content)
				b.WriteString("\n")
				addEntry(msg, i, -1, 2, tview.TaggedStringWidth(msg.Content), strings.Count(b.String()[prevLen:], "\n"))
			}
			continue
		}
		// Skip the assistant message box when there is no content yet.
		skipBox := msg.Role == session.RoleAssistant && msg.Content == "" && msg.Error == nil
		if !skipBox {
			if !first {
				b.WriteString("\n")
				lineCount++
			}
			first = false
			prevLen := b.Len()
			eIdx := len(renderedMsgs)
			lp, bw := cv.renderMessageBox(&b, msg, width, maxW, eIdx == cv.selectedIdx)
			addEntry(msg, i, -1, lp, bw, strings.Count(b.String()[prevLen:], "\n"))
		}

		// Tool calls: one summary line (collapsed) or one expanded box (expanded).
		if msg.Role == session.RoleAssistant && len(msg.ToolUses) > 0 {
			if !first {
				b.WriteString("\n")
				lineCount++
			}
			first = false
			prevLen := b.Len()
			if msg.ToolGroupExpanded {
				// Single expanded box containing all tool params.
				var boxTitle string
				if len(msg.ToolUses) == 1 {
					boxTitle = toolSingleLabel(msg.ToolUses[0])
				} else {
					boxTitle = toolGroupLabel(msg.ToolUses)
				}
				entry := session.Message{
					Role:          session.RoleAssistant,
					ExpandedBox:   true,
					BoxTitle:      boxTitle,
					Content:       formatAllToolParams(msg.ToolUses),
					ContentTagged: true,
				}
				eIdx2 := len(renderedMsgs)
				lp2, bw2 := cv.renderMessageBox(&b, entry, width, maxW, eIdx2 == cv.selectedIdx)
				addEntry(entry, i, -2, lp2, bw2, strings.Count(b.String()[prevLen:], "\n"))
			} else {
				// One aggregate summary line.
				line := toolGroupLabel(msg.ToolUses)
				entry := session.Message{Role: session.RoleStatus, Content: line}
				b.WriteString("  ")
				b.WriteString(line)
				b.WriteString("\n")
				addEntry(entry, i, -2, 2, tview.TaggedStringWidth(line), strings.Count(b.String()[prevLen:], "\n"))
			}
		}
	}

	cv.renderedMsgs = renderedMsgs
	cv.renderedMsgSessIdx = renderedMsgSessIdx
	cv.renderedMsgToolIdx = renderedMsgToolIdx
	cv.msgStartLine = msgStartLine
	cv.boxLeft = boxLeft
	cv.boxRight = boxRight

	// Build per-column copy mask and soft-wrap map for all message content lines.
	// Absent key = fully copyable. Present key: false columns are non-copyable
	// (format/border chars OR trailing blank padding inside the box).
	cv.copyColMask = make(map[int][]bool)
	cv.softWrapLine = make(map[int]bool)
	cw := maxW - 4 // maxContentW shared across all messages

	for i, msg := range renderedMsgs {
		start := msgStartLine[i]
		switch msg.Role {

		case session.RoleUser:
			lines := tview.WordWrap(tview.Escape(msg.Content), cw)
			actualW := 0
			for _, l := range lines {
				if n := tview.TaggedStringWidth(l); n > actualW {
					actualW = n
				}
			}
			boxCW := max(4, actualW) // boxW min 8 → contentW min 4
			for j, l := range lines {
				vlen := tview.TaggedStringWidth(l)
				if vlen < boxCW {
					cv.copyColMask[start+1+j] = allCopyMask(vlen)
				}
			}
			// Soft-wrap tracking: lines within the same \n-paragraph are wrapped,
			// lines at a \n boundary are hard breaks.
			offset := 0
			for _, para := range strings.Split(msg.Content, "\n") {
				wrapped := tview.WordWrap(tview.Escape(para), cw)
				for k := 0; k < len(wrapped)-1; k++ {
					cv.softWrapLine[start+1+offset+k] = true
				}
				offset += len(wrapped)
			}

		case session.RoleAssistant:
			if msg.ExpandedBox {
				// Plain-text or pre-tagged content (reasoning or tool params).
				escapedContent := msg.Content
				if !msg.ContentTagged {
					escapedContent = tview.Escape(msg.Content)
				}
				lines := tview.WordWrap(escapedContent, cw)
				actualW := 0
				for _, l := range lines {
					if n := tview.TaggedStringWidth(l); n > actualW {
						actualW = n
					}
				}
				boxCW := max(1, actualW)
				for j, l := range lines {
					if vlen := tview.TaggedStringWidth(l); vlen < boxCW {
						cv.copyColMask[start+1+j] = allCopyMask(vlen)
					}
				}
				offset := 0
				for _, para := range strings.Split(escapedContent, "\n") {
					wrapped := tview.WordWrap(para, cw)
					for k := 0; k < len(wrapped)-1; k++ {
						cv.softWrapLine[start+1+offset+k] = true
					}
					offset += len(wrapped)
				}
			} else if msg.Error != nil {
				lines := tview.WordWrap(tview.Escape(msg.Error.Error()), cw)
				actualW := 0
				for _, l := range lines {
					if n := tview.TaggedStringWidth(l); n > actualW {
						actualW = n
					}
				}
				boxCW := max(6, actualW) // boxW min 10 → contentW min 6
				for j, l := range lines {
					vlen := tview.TaggedStringWidth(l)
					if vlen < boxCW {
						cv.copyColMask[start+1+j] = allCopyMask(vlen)
					}
				}
				offset := 0
				for _, para := range strings.Split(msg.Error.Error(), "\n") {
					wrapped := tview.WordWrap(tview.Escape(para), cw)
					for k := 0; k < len(wrapped)-1; k++ {
						cv.softWrapLine[start+1+offset+k] = true
					}
					offset += len(wrapped)
				}
			} else if msg.Content != "" {
				blocks := cv.mdCache[msg.Content]
				if blocks == nil {
					break
				}
				rls := renderBlocksAnnotated(blocks, cw)
				actualW := 0
				for _, rl := range rls {
					if n := len(rl.copyMask); n > actualW {
						actualW = n
					}
				}
				minBW := 9
				if msg.Partial {
					minBW = 11
				}
				boxCW := max(minBW-4, actualW)
				for j, rl := range rls {
					mask := rl.copyMask
					hasFormat := false
					for _, v := range mask {
						if !v {
							hasFormat = true
							break
						}
					}
					if hasFormat || len(mask) < boxCW {
						cv.copyColMask[start+1+j] = mask
					}
					if rl.softWrap {
						cv.softWrapLine[start+1+j] = true
					}
				}
			}
		}
	}

	cv.appendRetryCountdown(&b)
	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

// appendRetryCountdown renders the ephemeral retry countdown line, if active.
func (cv *ChatView) appendRetryCountdown(b *strings.Builder) {
	if cv.currentStatus != "" {
		b.WriteString("\n  ")
		b.WriteString(cv.currentStatus)
		b.WriteString("\n")
	}
}

// renderMessageBox writes a single bordered message box into b.
//
// The box width is compact: sized to the actual content, capped at maxW.
// User boxes are flush to the right edge; assistant boxes are flush to the left.
//
// Box anatomy:
//
//	┌─ label ──────────┐
//	│ content line     │
//	└──────────────────┘
//
// renderMessageBox writes a bordered message box into b and returns the box's
// leftPad and boxW so the caller can record geometry without a separate pass.
// It uses cv.mdCache so that resize re-wraps without re-parsing markdown.
func (cv *ChatView) renderMessageBox(b *strings.Builder, msg session.Message, width, maxW int, isHovered bool) (leftPad, boxW int) {
	borderColor := Theme.Border
	if isHovered {
		borderColor = Theme.BorderFocus
	}
	bc := borderColor.CSS()

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
		lines := tview.WordWrap(tview.Escape(msg.Content), maxContentW)
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
		uc := TC.UserColor
		boxLine := mkBoxLine(contentW)

		// ┌─ you ──...──┐  "─ you " = 6 visible cols
		b.WriteString(p + fmt.Sprintf("[%s]┌─ [%s]you [%s]%s┐[-]", bc, uc, bc, dash(boxW-8)) + "\n")
		for _, line := range lines {
			b.WriteString(p + boxLine(line, tview.TaggedStringWidth(line)) + "\n")
		}
		b.WriteString(p + fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")

	case session.RoleAssistant:
		ac := TC.ApexColor
		mc := TC.Muted

		if msg.Error != nil {
			ec := TC.ErrorColor
			maxContentW := maxW - 4
			lines := tview.WordWrap(tview.Escape(msg.Error.Error()), maxContentW)
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
				inner := fmt.Sprintf("[%s]%s[-]", ec, line)
				b.WriteString(boxLine(inner, tview.TaggedStringWidth(line)) + "\n")
			}
			b.WriteString(fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")
			return
		}

		// ExpandedBox: dotted borders, BoxTitle label, plain-text content.
		// Normal: solid borders, "apex" label, markdown content.
		hChar, vChar := "─", "│"
		if msg.ExpandedBox {
			hChar, vChar = "╌", "╎"
		}
		fill := func(n int) string {
			if n < 0 {
				n = 0
			}
			return strings.Repeat(hChar, n)
		}
		mkBoxLineV := func(contentW int) func(inner string, vlen int) string {
			return func(inner string, vlen int) string {
				pad := contentW - vlen
				if pad < 0 {
					pad = 0
				}
				return fmt.Sprintf("[%s]%s[-] %s[-:-:-]%s [%s]%s[-]", bc, vChar, inner, strings.Repeat(" ", pad), bc, vChar)
			}
		}

		maxContentW := maxW - 4
		var lines []string
		if msg.ExpandedBox {
			content := msg.Content
			if !msg.ContentTagged {
				content = tview.Escape(content)
			}
			lines = tview.WordWrap(content, maxContentW)
		} else {
			blocks, ok := cv.mdCache[msg.Content]
			if !ok {
				blocks = parseMarkdown(msg.Content)
				cv.mdCache[msg.Content] = blocks
			}
			lines = renderBlocks(blocks, maxContentW)
		}
		actualW := 0
		for _, l := range lines {
			if n := tview.TaggedStringWidth(l); n > actualW {
				actualW = n
			}
		}

		partialFrag := ""
		extraW := 0
		if msg.Partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", mc)
			extraW = 2
		}

		// ExpandedBox: "┌╌ {BoxTitle} ┐" — fixed = titleW + 5 ("┌╌ " + " " + "┐")
		// Normal:      "┌─ apex ┐"     — fixed = 9; with partial: 11
		var minBoxW int
		if msg.ExpandedBox {
			minBoxW = tview.TaggedStringWidth(msg.BoxTitle) + 5
		} else if msg.Partial {
			minBoxW = 11
		} else {
			minBoxW = 9
		}
		boxW = max(minBoxW+extraW, actualW+4)
		contentW := boxW - 4
		boxLine := mkBoxLineV(contentW)

		if msg.ExpandedBox {
			titleW := tview.TaggedStringWidth(msg.BoxTitle)
			fmt.Fprintf(b, "[%s]┌╌ %s %s[%s]%s┐[-]\n",
				bc, msg.BoxTitle, partialFrag, bc, fill(boxW-titleW-5-extraW))
			if len(lines) == 0 {
				b.WriteString(boxLine("", 0) + "\n")
			}
		} else {
			fmt.Fprintf(b, "[%s]┌─ [%s]apex [%s]%s[%s]%s┐[-]\n",
				bc, ac, bc, partialFrag, bc, fill(boxW-9-extraW))
		}
		for _, line := range lines {
			b.WriteString(boxLine(line, tview.TaggedStringWidth(line)) + "\n")
		}
		fmt.Fprintf(b, "[%s]└%s┘[-]\n", bc, fill(boxW-2))
	}
	return
}

// toolKeyArg extracts the primary display value from a tool call's JSON arguments.
func toolKeyArg(name, input string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return ""
	}
	field := "path"
	switch name {
	case "run_shell":
		field = "command"
	case "web_fetch":
		field = "url"
	case "search_files":
		field = "pattern"
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(v, &s) != nil {
		return ""
	}
	runes := []rune(s)
	if len(runes) > 50 {
		return string(runes[:47]) + "…"
	}
	return s
}

type toolCat struct{ verb, infin, noun, nouns string }

var toolCats = map[string]toolCat{
	"read_file":      {"read", "read", "file", "files"},
	"write_file":     {"wrote", "write", "file", "files"},
	"edit_file":      {"edited", "edit", "file", "files"},
	"list_directory": {"listed", "list", "directory", "directories"},
	"run_shell":      {"ran", "run", "command", "commands"},
	"web_fetch":      {"fetched", "fetch", "URL", "URLs"},
	"search_files":   {"searched for", "search for", "pattern", "patterns"},
}

// toolGroupLabel generates a natural-language tview-tagged summary for a set of tool uses.
// Single tool delegates to toolSingleLabel; multiple tools produce an aggregate count line.
func toolGroupLabel(tools []session.ToolUse) string {
	if len(tools) == 0 {
		return ""
	}
	if len(tools) == 1 {
		return toolSingleLabel(tools[0])
	}
	ac, mc := TC.ApexDim, TC.Muted
	apex := fmt.Sprintf("[%s]apex[-][%s] ", ac, mc)

	type group struct {
		cat   toolCat
		count int
	}
	var groups []group
	seen := map[string]int{}
	for _, tu := range tools {
		cat, ok := toolCats[tu.Name]
		if !ok {
			cat = toolCat{"called", "call", "tool", "tools"}
		}
		if idx, exists := seen[tu.Name]; exists {
			groups[idx].count++
		} else {
			seen[tu.Name] = len(groups)
			groups = append(groups, group{cat: cat, count: 1})
		}
	}
	parts := make([]string, len(groups))
	for i, g := range groups {
		if g.count == 1 {
			parts[i] = fmt.Sprintf("%s 1 %s", g.cat.verb, g.cat.noun)
		} else {
			parts[i] = fmt.Sprintf("%s %d %s", g.cat.verb, g.count, g.cat.nouns)
		}
	}
	var desc string
	switch len(parts) {
	case 1:
		desc = parts[0]
	case 2:
		desc = parts[0] + " and " + parts[1]
	default:
		desc = strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
	return fmt.Sprintf("%s%s[-]", apex, desc)
}

// toolSingleLabel generates a natural-language tview-tagged summary for one tool use.
func toolSingleLabel(tu session.ToolUse) string {
	ac, mc := TC.ApexDim, TC.Muted
	apex := fmt.Sprintf("[%s]apex[-][%s] ", ac, mc)
	cat, ok := toolCats[tu.Name]
	key := toolKeyArg(tu.Name, tu.Input)
	if key == "" {
		key = tu.Name
	}
	var desc string
	switch {
	case !ok:
		desc = "used " + tu.Name
	case tu.State == "pending":
		desc = fmt.Sprintf("wants to %s %s", cat.infin, key)
	case tu.State == "error":
		desc = fmt.Sprintf("failed to %s %s", cat.infin, key)
	default:
		desc = fmt.Sprintf("%s %s", cat.verb, key)
	}
	return fmt.Sprintf("%s%s[-]", apex, desc)
}

// formatToolParams renders tool input JSON as highlighted key: value lines.
func formatToolParams(name, input string) string {
	if input == "" || input == "{}" {
		return ""
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &m); err != nil {
		return input
	}

	lc := TC.ToolColor // label color

	var sb strings.Builder
	written := 0

	writeField := func(key string, raw json.RawMessage) {
		if written > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "[%s]%s:[-] ", lc, key)
		switch {
		case len(raw) > 0 && raw[0] == '[':
			var arr []json.RawMessage
			_ = json.Unmarshal(raw, &arr)
			fmt.Fprintf(&sb, "[%d items]", len(arr))
		case len(raw) > 0 && raw[0] == '{':
			sb.WriteString("{...}")
		default:
			var s string
			if json.Unmarshal(raw, &s) == nil {
				runes := []rune(s)
				if len(runes) > 200 {
					sb.WriteString(tview.Escape(string(runes[:197]) + "…"))
				} else {
					sb.WriteString(tview.Escape(s))
				}
			} else {
				sb.Write(raw)
			}
		}
		written++
	}

	// Primary key first.
	primary := "path"
	switch name {
	case "run_shell":
		primary = "command"
	case "web_fetch":
		primary = "url"
	case "search_files":
		primary = "pattern"
	}
	if v, ok := m[primary]; ok {
		writeField(primary, v)
		delete(m, primary)
	}

	// Secondary fields in a stable order, reasoning always last.
	for _, k := range []string{"offset", "limit", "format", "timeout", "edits", "content"} {
		if v, ok := m[k]; ok {
			writeField(k, v)
			delete(m, k)
		}
	}
	reasoning, hasReasoning := m["reasoning"]
	delete(m, "reasoning")
	for k, v := range m {
		writeField(k, v)
	}
	if hasReasoning {
		writeField("reasoning", reasoning)
	}

	return sb.String()
}

// formatAllToolParams formats all tool uses in a group into a single tagged string
// with a name header and key-value params for each tool, separated by blank lines.
func formatAllToolParams(tools []session.ToolUse) string {
	if len(tools) == 1 {
		return formatToolParams(tools[0].Name, tools[0].Input)
	}
	var sb strings.Builder
	for i, tu := range tools {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[%s]%s[-]\n", TC.ToolColor, tview.Escape(tu.Name))
		sb.WriteString(formatToolParams(tu.Name, tu.Input))
	}
	return sb.String()
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
	maxW := cv.lastWidth * 4 / 5
	if maxW < 20 {
		maxW = 20
	}

	lastMsgIdx := -2
	var allLines []string
	var result strings.Builder
	lastContribDocLine := -1

	cv.iterPartialSel(func(docLine, colLo, colHi int, mask []bool) {
		msgIdx := cv.findMsgAt(docLine)
		if msgIdx < 0 || msgIdx >= len(cv.renderedMsgs) {
			return
		}
		if msgIdx != lastMsgIdx {
			lastMsgIdx = msgIdx
			allLines, _ = computeMsgContent(cv.renderedMsgs[msgIdx], cv.lastWidth, maxW)
		}
		lineWithinBox := docLine - cv.msgStartLine[msgIdx] - 1
		if lineWithinBox < 0 || lineWithinBox >= len(allLines) {
			return
		}
		line := allLines[lineWithinBox]
		var sb strings.Builder
		col := 0
		for _, r := range line {
			rW := tview.TaggedStringWidth(string(r))
			if col >= colHi {
				break
			}
			if col >= colLo {
				if mask == nil || (col < len(mask) && mask[col]) {
					sb.WriteRune(r)
				}
			}
			col += rW
		}
		s := sb.String()
		if s == "" {
			return
		}
		if lastContribDocLine >= 0 {
			// Use a space if the previous contributing line was a soft wrap
			// directly into this one; otherwise a real newline.
			if cv.softWrapLine[lastContribDocLine] && docLine == lastContribDocLine+1 {
				result.WriteByte(' ')
			} else {
				result.WriteByte('\n')
			}
		}
		result.WriteString(s)
		lastContribDocLine = docLine
	})

	return result.String()
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

	multi := hi > lo
	var parts []string
	for i := lo; i <= hi; i++ {
		msg := cv.renderedMsgs[i]
		var content string
		switch msg.Role {
		case session.RoleUser:
			content = msg.Content
		case session.RoleAssistant:
			if msg.Error != nil {
				content = msg.Error.Error()
			} else {
				content = msg.Content
			}
		}
		if multi {
			role := "assistant"
			if msg.Role == session.RoleUser {
				role = "you"
			} else if msg.ExpandedBox {
				role = "thoughts"
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
		lines := tview.WordWrap(tview.Escape(msg.Content), maxContentW)
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
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, lp
	case session.RoleAssistant:
		if msg.ExpandedBox {
			lines := tview.WordWrap(tview.Escape(msg.Content), maxContentW)
			for i, l := range lines {
				lines[i] = tview.Unescape(l)
			}
			return lines, 0
		}
		if msg.Error != nil {
			lines := tview.WordWrap(tview.Escape(msg.Error.Error()), maxContentW)
			for i, l := range lines {
				lines[i] = tview.Unescape(l)
			}
			return lines, 0
		}
		raw := renderMarkdown(msg.Content, maxContentW)
		all := make([]string, len(raw))
		for i, l := range raw {
			all[i] = stripTags(l)
		}
		return all, 0
	case session.RoleStatus:
		return []string{stripTags(msg.Content)}, 2
	}
	return nil, 0
}
