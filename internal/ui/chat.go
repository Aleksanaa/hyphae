package ui

import (
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

// renderedEntry records one displayed item with its session origin and screen geometry.
type renderedEntry struct {
	msg       session.Message
	sessIdx   int
	toolIdx   int
	lineStart int // doc-line of the item's top border
	boxLeft   int // left-pad columns
	boxRight  int // boxLeft + boxWidth
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
	TotalLines      int             // read by Scrollbar
	hoverIdx        int             // index into renderedMsgs; -1 = none
	selectedIdx     int             // box highlighted by last click; -1 = none
	lastSelectedIdx int             // selectedIdx at last buildText call; -2 = never built
	entries         []renderedEntry // one per displayed item (box or flat line)

	// callbacks for double-clicking expandable items
	onStatusExpand    func(sessionIdx int)
	onToolGroupExpand func(sessionIdx int)

	// drag-to-select state
	selAnchor    selPoint
	selCursor    selPoint
	selActive    bool
	dragging     bool
	anchorBox    int // index into entries where drag started (-1 = none)
	selCursorBox int // last entries index the cursor touched during drag

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
	if !cv.selActive || cv.anchorBox < 0 || cv.anchorBox >= len(cv.entries) {
		return false
	}
	startDoc, endDoc := cv.boxDocRange(cv.anchorBox)
	cl := cv.selCursor.docLine
	return cl <= startDoc || cl >= endDoc-1
}

// boxDocRange returns the [startDoc, endDoc) doc-line range for box i,
// excluding the blank separator line that follows each non-last box.
func (cv *ChatView) boxDocRange(i int) (startDoc, endDoc int) {
	startDoc = cv.entries[i].lineStart
	if i+1 < len(cv.entries) {
		endDoc = cv.entries[i+1].lineStart - 1
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
	if cv.anchorBox < 0 || cv.anchorBox >= len(cv.entries) {
		return
	}
	ix, _, _, _ := cv.GetInnerRect()
	ae := cv.entries[cv.anchorBox]
	contentLeft := ix + ae.boxLeft + 2
	contentRight := ix + ae.boxRight - 2

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
	if cv.anchorBox < 0 || cv.anchorBox >= len(cv.entries) {
		return
	}
	ix, iy, _, ih := cv.GetInnerRect()
	contentLeft := ix + cv.entries[cv.anchorBox].boxLeft + 2
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
	if hi < 0 || hi >= len(cv.entries) {
		hi = len(cv.entries) - 1
	}

	ix, iy, iw, ih := cv.GetInnerRect()
	scrollY, _ := cv.GetScrollOffset()

	selStyle := tcell.StyleDefault.
		Foreground(tcell.NewRGBColor(220, 220, 230)).
		Background(tcell.NewRGBColor(50, 80, 150))

	for msgIdx := lo; msgIdx <= hi; msgIdx++ {
		if msgIdx >= len(cv.entries) {
			break
		}
		e := cv.entries[msgIdx]
		startDoc, endDoc := cv.boxDocRange(msgIdx)
		bLeft := ix + e.boxLeft
		bRight := ix + e.boxRight

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
	cv.buildText(w)
	if atBottom {
		cv.TextView.ScrollToEnd()
	} else {
		cv.TextView.ScrollTo(scrollY, 0)
	}
}

// HoveredContent returns the raw content of whichever message the mouse is over.
func (cv *ChatView) HoveredContent() string {
	if cv.hoverIdx < 0 || cv.hoverIdx >= len(cv.entries) {
		return ""
	}
	return cv.entries[cv.hoverIdx].msg.Content
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
			cv.handleDoubleClick(docLine)
		}

		return consumed, capture
	}
}

func (cv *ChatView) handleDoubleClick(docLine int) {
	idx := cv.findMsgAt(docLine)
	if idx < 0 || idx >= len(cv.entries) {
		return
	}
	e := cv.entries[idx]
	if (e.toolIdx >= 0 || e.toolIdx == -2) && cv.onToolGroupExpand != nil {
		cv.onToolGroupExpand(e.sessIdx)
	} else if ((e.msg.Role == session.RoleStatus && e.msg.Content != "") || e.msg.ExpandedBox) && cv.onStatusExpand != nil {
		cv.onStatusExpand(e.sessIdx)
	}
}

func (cv *ChatView) findMsgAt(docLine int) int {
	for i, e := range cv.entries {
		end := cv.TotalLines
		if i+1 < len(cv.entries) {
			end = cv.entries[i+1].lineStart
		}
		if docLine >= e.lineStart && docLine < end {
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
	if len(cv.entries) == 0 {
		return
	}
	for i := range cv.entries {
		startDoc, endDoc := cv.boxDocRange(i)
		if docLine >= startDoc && docLine < endDoc {
			cv.selCursorBox = i
			return
		}
	}
	// Cursor is above all boxes or below all boxes — snap to edge.
	// In a separator gap between boxes, keep the previous selCursorBox.
	if docLine < cv.entries[0].lineStart {
		cv.selCursorBox = 0
	} else if docLine >= cv.TotalLines {
		cv.selCursorBox = len(cv.entries) - 1
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
	const subtitle = "terminal coding agent"

	artW := 0
	for _, line := range hyphaeArt {
		if w := len(line); w > artW {
			artW = w
		}
	}

	for range max(0, (viewH-len(hyphaeArt)-2)/2) {
		b.WriteByte('\n')
	}
	pad := strings.Repeat(" ", max(0, (width-artW)/2))
	for _, line := range hyphaeArt {
		fmt.Fprintf(b, "[%s]%s%s[-]\n", TC.Accent, pad, tview.Escape(line))
	}
	b.WriteByte('\n')
	fmt.Fprintf(b, "[%s]%s%s[-]\n", TC.Muted, strings.Repeat(" ", max(0, (width-len(subtitle))/2)), subtitle)
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
		cv.entries = nil
		text := b.String()
		cv.TotalLines = strings.Count(text, "\n") + 1
		cv.TextView.SetText(text)
		return
	}

	var b strings.Builder
	var entries []renderedEntry
	first := true
	lineCount := 0

	addEntry := func(entry session.Message, sessIdx, toolIdx, lp, bw, lines int) {
		entries = append(entries, renderedEntry{
			msg: entry, sessIdx: sessIdx, toolIdx: toolIdx,
			lineStart: lineCount, boxLeft: lp, boxRight: lp + bw,
		})
		lineCount += lines
	}
	msgs := cv.messages
	mn := len(msgs)

	sep := func() {
		if !first {
			b.WriteString("\n")
			lineCount++
		}
		first = false
	}
	writeFlatLine := func(line string, sessIdx, toolIdx int) {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
		addEntry(session.Message{Role: session.RoleStatus, Content: line}, sessIdx, toolIdx, 2, tview.TaggedStringWidth(line), 1)
	}
	renderBox := func(entry session.Message, sessIdx, toolIdx int) {
		prev := b.Len()
		lp, bw := cv.renderMessageBox(&b, entry, width, maxW, len(entries) == cv.selectedIdx)
		addEntry(entry, sessIdx, toolIdx, lp, bw, strings.Count(b.String()[prev:], "\n"))
	}

	for mi := 0; mi < mn; mi++ {
		msg := msgs[mi]
		if msg.Role == session.RoleTool {
			continue
		}
		if msg.Role == session.RoleStatus {
			var secs int
			var contentParts []string
			var allTools []session.ToolUse
			var hasThinking bool
			j := mi
			for j < mn {
				for j < mn && msgs[j].Role == session.RoleTool {
					j++
				}
				if j >= mn || msgs[j].Role != session.RoleStatus {
					break
				}
				roundStart := j
				sm := msgs[j]
				j++
				for j < mn && msgs[j].Role == session.RoleTool {
					j++
				}
				if j >= mn || msgs[j].Role != session.RoleAssistant {
					break
				}
				am := msgs[j]
				if am.Content != "" || am.Error != nil || am.Partial {
					break
				}
				// Rounds with pending/error tools render separately so the correct
				// per-state verb is used ("wants to run" vs "ran") and they stay visible.
				hasPendingOrError := false
				for _, tu := range am.ToolUses {
					if tu.State == "pending" || tu.State == "refused" || tu.State == "error" {
						hasPendingOrError = true
						break
					}
				}
				if hasPendingOrError {
					j = roundStart
					break
				}
				secs += sm.ThinkingSecs
				if am.Thinking != "" {
					hasThinking = true
					contentParts = append(contentParts, tview.Escape(am.Thinking))
				}
				if len(am.ToolUses) > 0 {
					contentParts = append(contentParts, formatAllToolParams(am.ToolUses))
					allTools = append(allTools, am.ToolUses...)
				}
				j++
			}

			if hasThinking {
				sep()
				desc := "thought for a moment"
				if secs > 0 {
					desc = fmt.Sprintf("thought for %ds", secs)
				}
				if len(allTools) > 0 {
					desc += ", " + toolGroupDesc(allTools)
				}
				if msgs[mi].ThinkingExpanded {
					title := fmt.Sprintf("[%s]apex[-][%s] (thoughts)[-]", TC.ApexColor, TC.Muted)
					if secs > 0 {
						title = fmt.Sprintf("[%s]apex[-][%s] (thoughts, %ds)[-]", TC.ApexColor, TC.Muted, secs)
					}
					renderBox(session.Message{
						Role: session.RoleAssistant, ExpandedBox: true,
						BoxTitle: title, Content: strings.Join(contentParts, "\n\n"), ContentTagged: true,
					}, mi, -1)
				} else {
					writeFlatLine(apexLabel(desc), mi, -1)
				}
				mi = j - 1
				continue
			}

			if msg.Content == "" {
				continue
			}
			sep()
			if msg.ThinkingExpanded {
				thinking, partial := "", false
				for jj := mi + 1; jj < mn; jj++ {
					if msgs[jj].Role == session.RoleAssistant {
						thinking, partial = msgs[jj].Thinking, msgs[jj].Partial
						break
					}
				}
				renderBox(session.Message{
					Role: session.RoleAssistant, Content: thinking, Partial: partial,
					ExpandedBox: true, BoxTitle: fmt.Sprintf("[%s]apex[-][%s] (thoughts)[-]", TC.ApexColor, TC.Muted),
				}, mi, -1)
			} else {
				writeFlatLine(msg.Content, mi, -1)
			}
			continue
		}

		if msg.Role != session.RoleAssistant || msg.Content != "" || msg.Error != nil {
			sep()
			renderBox(msg, mi, -1)
		}

		if msg.Role == session.RoleAssistant && len(msg.ToolUses) > 0 {
			sep()
			if msg.ToolGroupExpanded {
				renderBox(session.Message{
					Role:          session.RoleAssistant,
					ExpandedBox:   true,
					BoxTitle:      toolGroupLabel(msg.ToolUses),
					Content:       formatAllToolParams(msg.ToolUses),
					ContentTagged: true,
				}, mi, -2)
			} else {
				var collapsible, individual []session.ToolUse
				for _, tu := range msg.ToolUses {
					if tu.State == "pending" || tu.State == "refused" || tu.State == "error" {
						individual = append(individual, tu)
					} else {
						collapsible = append(collapsible, tu)
					}
				}
				if len(collapsible) > 0 {
					writeFlatLine(toolGroupLabel(collapsible), mi, -2)
				}
				for _, tu := range individual {
					writeFlatLine(toolSingleLabel(tu), mi, -2)
				}
			}
		}
	}

	cv.entries = entries
	cv.buildCopyMasks(entries, maxW)

	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

// buildCopyMasks populates copyColMask and softWrapLine for all rendered messages.
// copyColMask[line] is absent when the line is fully copyable; present with a
// []bool mask when trailing padding or format chars must be excluded from copies.
func (cv *ChatView) buildCopyMasks(entries []renderedEntry, maxW int) {
	cv.copyColMask = make(map[int][]bool)
	cv.softWrapLine = make(map[int]bool)
	cw := maxW - 4

	maskWordWrap := func(start, minCW int, content string) {
		lines := tview.WordWrap(content, cw)
		boxCW := max(minCW, maxTaggedWidth(lines))
		for j, l := range lines {
			if vlen := tview.TaggedStringWidth(l); vlen < boxCW {
				cv.copyColMask[start+1+j] = allCopyMask(vlen)
			}
		}
		offset := 0
		for _, para := range strings.Split(content, "\n") {
			wrapped := tview.WordWrap(para, cw)
			for k := 0; k < len(wrapped)-1; k++ {
				cv.softWrapLine[start+1+offset+k] = true
			}
			offset += len(wrapped)
		}
	}

	for _, e := range entries {
		start, msg := e.lineStart, e.msg
		switch msg.Role {
		case session.RoleUser:
			maskWordWrap(start, 4, tview.Escape(msg.Content))
		case session.RoleAssistant:
			switch {
			case msg.ExpandedBox:
				content := msg.Content
				if !msg.ContentTagged {
					content = tview.Escape(content)
				}
				maskWordWrap(start, 1, content)
			case msg.Error != nil:
				maskWordWrap(start, 6, tview.Escape(msg.Error.Error()))
			case msg.Content != "":
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
}

// maxTaggedWidth returns the maximum visual width across tview-tagged lines.
func maxTaggedWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := tview.TaggedStringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// renderMessageBox writes a compact bordered box into b and returns leftPad and boxW.
// Box anatomy:  ┌─ label ───┐ / │ content │ / └───────────┘
// ExpandedBox uses dotted borders (╌/╎) and BoxTitle; normal uses solid and "apex".
func (cv *ChatView) renderMessageBox(b *strings.Builder, msg session.Message, width, maxW int, isHovered bool) (leftPad, boxW int) {
	bc := Theme.Border.CSS()
	if isHovered {
		bc = Theme.BorderFocus.CSS()
	}

	hChar, vChar := "─", "│"
	if msg.ExpandedBox {
		hChar, vChar = "╌", "╎"
	}
	fill := func(n int) string { return strings.Repeat(hChar, max(0, n)) }
	// mkLine pads inner to contentW; [-:-:-] resets style before padding so
	// trailing spaces and the border char are not colored by inner tags.
	mkLine := func(contentW int) func(string, int) string {
		return func(inner string, vlen int) string {
			return fmt.Sprintf("[%s]%s[-] %s[-:-:-]%s [%s]%s[-]", bc, vChar, inner, strings.Repeat(" ", max(0, contentW-vlen)), bc, vChar)
		}
	}

	maxContentW := maxW - 4

	switch msg.Role {
	case session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.Content), maxContentW)
		boxW = max(8, maxTaggedWidth(lines)+4)
		leftPad = max(0, width-boxW)
		p := strings.Repeat(" ", leftPad)
		boxLine := mkLine(boxW - 4)
		fmt.Fprintf(b, "%s[%s]┌─ [%s]you [%s]%s┐[-]\n", p, bc, TC.UserColor, bc, fill(boxW-8))
		for _, line := range lines {
			fmt.Fprintf(b, "%s%s\n", p, boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))

	case session.RoleAssistant:
		if msg.Error != nil {
			lines := tview.WordWrap(tview.Escape(msg.Error.Error()), maxContentW)
			boxW = max(10, maxTaggedWidth(lines)+4)
			boxLine := mkLine(boxW - 4)
			fmt.Fprintf(b, "[%s]┌─ [%s]error [%s]%s┐[-]\n", bc, TC.ErrorColor, bc, fill(boxW-10))
			for _, line := range lines {
				fmt.Fprintf(b, "%s\n", boxLine(fmt.Sprintf("[%s]%s[-]", TC.ErrorColor, line), tview.TaggedStringWidth(line)))
			}
			fmt.Fprintf(b, "[%s]└%s┘[-]\n", bc, fill(boxW-2))
			return
		}

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

		partialFrag, extraW := "", 0
		if msg.Partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", TC.Muted)
			extraW = 2
		}
		minBoxW := 9
		if msg.ExpandedBox {
			minBoxW = tview.TaggedStringWidth(msg.BoxTitle) + 5
		} else if msg.Partial {
			minBoxW = 11
		}
		boxW = max(minBoxW+extraW, maxTaggedWidth(lines)+4)
		boxLine := mkLine(boxW - 4)

		if msg.ExpandedBox {
			fmt.Fprintf(b, "[%s]┌╌ %s %s[%s]%s┐[-]\n",
				bc, msg.BoxTitle, partialFrag, bc, fill(boxW-tview.TaggedStringWidth(msg.BoxTitle)-5-extraW))
			if len(lines) == 0 {
				fmt.Fprintf(b, "%s\n", boxLine("", 0))
			}
		} else {
			fmt.Fprintf(b, "[%s]┌─ [%s]apex [%s]%s[%s]%s┐[-]\n",
				bc, TC.ApexColor, bc, partialFrag, bc, fill(boxW-9-extraW))
		}
		for _, line := range lines {
			fmt.Fprintf(b, "%s\n", boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "[%s]└%s┘[-]\n", bc, fill(boxW-2))
	}
	return
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

// toolGroupDesc returns the plain-text action description for a set of tool uses,
// without any "apex" prefix or tview tags.
func toolGroupDesc(tools []session.ToolUse) string {
	counts := map[string]int{}
	var names []string
	for _, tu := range tools {
		if counts[tu.Name] == 0 {
			names = append(names, tu.Name)
		}
		counts[tu.Name]++
	}
	parts := make([]string, len(names))
	for i, name := range names {
		cat, ok := toolCats[name]
		if !ok {
			cat = toolCat{verb: "called", noun: "tool", nouns: "tools"}
		}
		n := counts[name]
		if n == 1 {
			parts[i] = cat.verb + " 1 " + cat.noun
		} else {
			parts[i] = fmt.Sprintf("%s %d %s", cat.verb, n, cat.nouns)
		}
	}
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1]
	}
}

// apexLabel wraps desc in the dim "apex" prefix with muted text color.
func apexLabel(desc string) string {
	return fmt.Sprintf("[%s]apex[-][%s] %s[-]", TC.ApexDim, TC.Muted, desc)
}

// toolGroupLabel generates a tview-tagged summary for a set of tool uses.
func toolGroupLabel(tools []session.ToolUse) string {
	if len(tools) == 0 {
		return ""
	}
	if len(tools) == 1 {
		return toolSingleLabel(tools[0])
	}
	return apexLabel(toolGroupDesc(tools))
}

// toolSingleLabel generates a tview-tagged summary for one tool use.
func toolSingleLabel(tu session.ToolUse) string {
	cat, ok := toolCats[tu.Name]
	key := tu.DisplayKey
	if key == "" {
		key = tu.Name
	}
	switch {
	case !ok:
		return apexLabel("used " + tu.Name)
	case tu.State == "pending":
		return apexLabel(fmt.Sprintf("wants to %s %s", cat.infin, key))
	case tu.State == "refused":
		return apexLabel(fmt.Sprintf("wanted to %s %s but was rejected", cat.infin, key))
	case tu.State == "error":
		return apexLabel(fmt.Sprintf("failed to %s %s", cat.infin, key))
	default:
		return apexLabel(fmt.Sprintf("%s %s", cat.verb, key))
	}
}

// renderToolParams formats a tool use's pre-parsed params as highlighted key: value lines.
func renderToolParams(tu session.ToolUse) string {
	var sb strings.Builder
	for i, p := range tu.DisplayParams {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "[%s]%s:[-] %s", TC.ToolColor, p.Key, tview.Escape(p.Value))
	}
	return sb.String()
}

// formatAllToolParams formats all tool uses in a group into a single tagged string,
// with a name header for each tool when there are multiple.
func formatAllToolParams(tools []session.ToolUse) string {
	if len(tools) == 1 {
		return renderToolParams(tools[0])
	}
	var sb strings.Builder
	for i, tu := range tools {
		if i > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[%s]%s[-]\n", TC.ToolColor, tview.Escape(tu.Name))
		sb.WriteString(renderToolParams(tu))
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
	maxW := max(20, cv.lastWidth*4/5)

	lastMsgIdx := -2
	var allLines []string
	var result strings.Builder
	lastContribDocLine := -1

	cv.iterPartialSel(func(docLine, colLo, colHi int, mask []bool) {
		msgIdx := cv.findMsgAt(docLine)
		if msgIdx < 0 || msgIdx >= len(cv.entries) {
			return
		}
		if msgIdx != lastMsgIdx {
			lastMsgIdx = msgIdx
			allLines, _ = computeMsgContent(cv.entries[msgIdx].msg, cv.lastWidth, maxW)
		}
		lineWithinBox := docLine - cv.entries[msgIdx].lineStart - 1
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
	if hi >= len(cv.entries) {
		hi = len(cv.entries) - 1
	}

	multi := hi > lo
	var parts []string
	for i := lo; i <= hi; i++ {
		msg := cv.entries[i].msg
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
func computeMsgContent(msg session.Message, width, maxW int) ([]string, int) {
	cw := maxW - 4
	switch msg.Role {
	case session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.Content), cw)
		boxW := max(8, maxTaggedWidth(lines)+4)
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, max(0, width-boxW)
	case session.RoleAssistant:
		var plain string
		switch {
		case msg.ExpandedBox:
			plain = msg.Content
		case msg.Error != nil:
			plain = msg.Error.Error()
		default:
			raw := renderMarkdown(msg.Content, cw)
			out := make([]string, len(raw))
			for i, l := range raw {
				out[i] = stripTags(l)
			}
			return out, 0
		}
		lines := tview.WordWrap(tview.Escape(plain), cw)
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, 0
	case session.RoleStatus:
		return []string{stripTags(msg.Content)}, 2
	}
	return nil, 0
}
