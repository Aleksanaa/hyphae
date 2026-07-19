package ui

import (
	"fmt"
	"strings"
	"time"

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
	msg       renderMsg
	sessIdx   int
	toolIdx   int
	lineStart int // doc-line of the item's top border
	boxLeft   int // left-pad columns
	boxRight  int // boxLeft + boxWidth
	// contentLines are the box's inner content lines (masks content-relative to
	// boxLeft+2, soft-wrap flags). Content line j sits at doc-line lineStart+1+j.
	// Nil for flat status lines and the compact divider (whole-select only).
	contentLines []renderedLine
}

// contentLineAt returns the content-line index for a doc-line and whether that
// doc-line is one of the entry's content lines (false for the top border/header,
// the bottom border, or a doc-line outside the entry). Boxes have a bottom border
// after the content; the thoughts rail has none — both are handled by the single
// [lineStart+1, lineStart+1+len(contentLines)) range with no per-kind branching.
func (e renderedEntry) contentLineAt(docLine int) (int, bool) {
	j := docLine - e.lineStart - 1
	if j >= 0 && j < len(e.contentLines) {
		return j, true
	}
	return 0, false
}

// toolIdxCompact is the sentinel toolIdx value for compact divider entries.
const toolIdxCompact = -3

// ChatView displays the conversation as individually bordered message boxes.
type ChatView struct {
	*tview.TextView
	messages        []session.Message
	lastWidth       int
	lastHeight      int             // inner height at last buildText; welcome recenters on change
	welcomeShown    bool            // last buildText rendered the (vertically-centred) welcome screen
	TotalLines      int             // read by Scrollbar
	forceBottom     bool            // next Render jumps to the last message regardless of scroll pos
	autoFollow      bool            // following the latest message; pins selectedIdx to the last box
	hoverIdx        int             // index into renderedMsgs; -1 = none
	selectedIdx     int             // box highlighted by last click; -1 = none
	lastSelectedIdx int             // selectedIdx at last buildText call; -2 = never built
	entries         []renderedEntry // one per displayed item (box or flat line)

	// compact divider state (toolIdx == toolIdxCompact in entries marks a divider; sessIdx = divider index)
	compactSummary  string
	compactSeqs     []int        // all compact atSeqs in order; nil = no compact
	compactExpanded map[int]bool // divider index → expanded state

	// status (thinking/tool) box expansion — UI-only display state, keyed by
	// session-message index. The UI owns display arrangement, so this lives here
	// rather than on session.Message.
	statusExpanded map[int]bool

	// drag-to-select state
	selAnchor    selPoint
	selCursor    selPoint
	selActive    bool
	dragging     bool
	anchorBox    int // index into entries where drag started (-1 = none)
	selCursorBox int // last entries index the cursor touched during drag

	// liveStatus holds transient connecting/thinking text set by controller events.
	// It is shown in the live status slot only when no StatusEvent provides live text.
	liveStatus string

	// welcomeArt is this view's mycelium banner, generated once per ChatView so
	// every new session tab shows a different network. welcomeFocal is the
	// wordmark's centre column, used to centre the art.
	welcomeArt   []string
	welcomeFocal int

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

// Restyle re-applies theme colors after a theme switch. The text style carries
// the default foreground (what "[-]" resets to) and the empty-cell background,
// both snapshotted from the global Styles at construction, so they must be
// re-applied here or plain body text keeps the previous theme's colors.
func (cv *ChatView) Restyle() {
	cv.SetBackgroundColor(Theme.Background)
	cv.SetTextStyle(tcell.StyleDefault.
		Foreground(Theme.Text).
		Background(Theme.Background))
}

// NewChatView creates the scrollable message display.
func NewChatView() *ChatView {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(false) // we manage layout manually
	tv.SetBorder(false)   // no outer frame; messages have their own boxes
	tv.SetBackgroundColor(Theme.Background)
	art, focal := buildHyphaeArt(time.Now().UnixNano())
	return &ChatView{
		TextView:        tv,
		hoverIdx:        -1,
		selectedIdx:     -1,
		lastSelectedIdx: -2,
		anchorBox:       -1,
		selCursorBox:    -1,
		mdCache:         make(map[string][]mdBlock),
		compactExpanded: make(map[int]bool),
		statusExpanded:  make(map[int]bool),
		welcomeArt:      art,
		welcomeFocal:    focal,
	}
}

// SetLiveStatus sets the transient connecting/thinking text shown in the live
// status slot when no StatusEvent provides a current operation to display.
func (cv *ChatView) SetLiveStatus(text string) { cv.liveStatus = text }

// SetCompact stores the compact state for rendering. Carries over expansion
// state for unchanged dividers; new dividers start collapsed.
func (cv *ChatView) SetCompact(summary string, seqs []int) {
	cv.compactSummary = summary
	newExpanded := make(map[int]bool, len(seqs))
	for i, s := range seqs {
		if i < len(cv.compactSeqs) && cv.compactSeqs[i] == s {
			newExpanded[i] = cv.compactExpanded[i]
		}
	}
	cv.compactSeqs = append([]int(nil), seqs...)
	cv.compactExpanded = newExpanded
}

// SetFocused is called by focus/blur hooks; no visible border to update.
func (cv *ChatView) SetFocused(_ bool) {}

// Draw rebuilds text when width, selection, or ephemeral status changes.
// Activity items from the session are updated via Render; no extra check needed.
func (cv *ChatView) Draw(screen tcell.Screen) {
	_, _, w, h := cv.GetInnerRect()
	// The welcome screen is vertically centred via leading padding derived from
	// the view height, so it must rebuild on height change too — otherwise the
	// stale padding leaves it stuck to the top or overflowing into a scrollbar.
	if w > 0 && (w != cv.lastWidth || cv.selectedIdx != cv.lastSelectedIdx || (cv.welcomeShown && h != cv.lastHeight)) {
		cv.lastWidth = w
		cv.lastHeight = h
		cv.lastSelectedIdx = cv.selectedIdx
		cv.buildText(w)
		if cv.welcomeShown {
			cv.TextView.ScrollToBeginning()
		}
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

// isWhole reports whether the cursor is on a border/header line or outside the
// anchor box, triggering whole-box-at-a-time selection mode. Anything that is not
// one of the anchor box's content lines selects whole boxes.
func (cv *ChatView) isWhole() bool {
	if !cv.selActive || cv.anchorBox < 0 || cv.anchorBox >= len(cv.entries) {
		return false
	}
	_, ok := cv.entries[cv.anchorBox].contentLineAt(cv.selCursor.docLine)
	return !ok
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
	// Clamp the right edge to the box's right column uniformly; each line's mask
	// (len < box width for short lines, borders/padding false) decides which
	// columns are actually copyable, so no per-kind border bookkeeping is needed.
	maxRight := ix + ae.boxRight

	anchor, cur := cv.selAnchor, cv.selCursor
	if anchor.docLine > cur.docLine ||
		(anchor.docLine == cur.docLine && anchor.screenX > cur.screenX) {
		anchor, cur = cur, anchor
	}

	for docLine := anchor.docLine; docLine <= cur.docLine; docLine++ {
		if _, ok := ae.contentLineAt(docLine); !ok {
			continue // top border / header / bottom border
		}
		x0 := contentLeft
		x1 := maxRight
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
		Foreground(Theme.Text).
		Background(chatSelBg)

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
		Foreground(Theme.Text).
		Background(chatSelBg)

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
	atBottom := cv.forceBottom || h <= 0 || scrollY+h >= cv.TotalLines
	cv.forceBottom = false
	cv.lastWidth = w
	cv.lastHeight = h
	cv.buildText(w)
	if atBottom {
		cv.TextView.ScrollToEnd()
	} else {
		cv.TextView.ScrollTo(scrollY, 0)
	}
}

// FollowLatest starts auto-following the latest message: the next Render jumps to
// the bottom, and buildText keeps selectedIdx pinned to the last box so it shows
// the same focus border as a clicked box, tracking the reply as it streams in.
// Following stops only when the user focuses another message (clicks a different
// box); scrolling and input focus leave it running.
func (cv *ChatView) FollowLatest() {
	cv.autoFollow = true
	cv.forceBottom = true
}

// StopFollow ends auto-follow and drops the pinned selection so the focus border
// clears on the next rebuild. No-op when not following, so it leaves a genuine
// click-selection untouched.
func (cv *ChatView) StopFollow() {
	if cv.autoFollow {
		cv.autoFollow = false
		cv.selectedIdx = -1
	}
}

// SettleFollow ends auto-follow when the turn goes idle, but leaves the last
// message's focus border in place as an ordinary selection — it then clears on
// the next mouse-move-out or click, like any clicked box.
func (cv *ChatView) SettleFollow() { cv.autoFollow = false }

// HoveredContent returns the raw content of whichever message the mouse is over.
func (cv *ChatView) HoveredContent() string {
	if cv.hoverIdx < 0 || cv.hoverIdx >= len(cv.entries) {
		return ""
	}
	return cv.entries[cv.hoverIdx].msg.content
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
			// Focusing another message ends auto-follow; clicking the followed
			// message (selectedIdx while following) leaves it running.
			if cv.hoverIdx >= 0 && cv.hoverIdx != cv.selectedIdx {
				cv.StopFollow()
			}
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
	if e.toolIdx == toolIdxCompact {
		divIdx := e.sessIdx
		// Only the last divider holds a valid summary; old ones are position-only markers.
		if divIdx == len(cv.compactSeqs)-1 {
			cv.compactExpanded[divIdx] = !cv.compactExpanded[divIdx]
			cv.buildText(cv.lastWidth)
		}
		return
	}
	if (e.msg.role.IsStatus() && e.msg.content != "") || e.msg.expandedBox {
		cv.statusExpanded[e.sessIdx] = !cv.statusExpanded[e.sessIdx]
		cv.buildText(cv.lastWidth)
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

// readableCap bounds how wide the conversation band grows, so lines keep a
// comfortable reading length and stay centered on very wide terminals.
const readableCap = 120

// convoLayout describes the centered conversation band and the per-role content
// column widths within it, all derived from the terminal width:
//   - band is the reading column (≤ readableCap); off is the left margin that centers it.
//   - user messages keep a 20% left gap (right-aligned within the band).
//   - assistant output keeps a 20% right gap (left-aligned) once the band exceeds
//     80 cols; below that it fills the band.
type convoLayout struct {
	band, off    int
	asstW, userW int
}

func newConvoLayout(width int) convoLayout {
	band := max(20, min(readableCap, width))
	asstW := band
	if band > 80 {
		asstW = max(20, band*4/5)
	}
	return convoLayout{
		band:  band,
		off:   max(0, (width-band)/2),
		asstW: asstW,
		userW: max(20, band*4/5),
	}
}

func (cv *ChatView) buildText(width int) {
	// Doc lines shift whenever text is rebuilt, so any live selection is invalid.
	cv.selActive = false
	cv.dragging = false
	cv.anchorBox = -1
	cv.selCursorBox = -1

	lay := newConvoLayout(width)

	hasDisplayable := false
	for _, msg := range cv.messages {
		if msg.Role != session.RoleTool {
			hasDisplayable = true
			break
		}
	}

	cv.welcomeShown = !hasDisplayable
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
	lastBoxIdx := -1 // entry index of the last rendered box (auto-follow target)

	addEntry := func(entry renderMsg, sessIdx, toolIdx, lp, bw, lines int, content []renderedLine) {
		entries = append(entries, renderedEntry{
			msg: entry, sessIdx: sessIdx, toolIdx: toolIdx,
			lineStart: lineCount, boxLeft: lp, boxRight: lp + bw,
			contentLines: content,
		})
		lineCount += lines
	}
	msgs := cv.messages

	sep := func() {
		if !first {
			b.WriteString("\n")
			lineCount++
		}
		first = false
	}
	// writeFlatLine renders a one-line status (collapsed rounds / live progress).
	// The entry is marked with a status role so double-click and selection treat it
	// as an expandable status.
	writeFlatLine := func(line string, sessIdx, toolIdx int) {
		line = stabilizeWidth(line)
		b.WriteString(strings.Repeat(" ", lay.off+2))
		b.WriteString(line)
		b.WriteString("\n")
		addEntry(renderMsg{role: session.RoleTool, content: line}, sessIdx, toolIdx, lay.off+2, tview.TaggedStringWidth(line), 1, nil)
	}
	renderBox := func(entry renderMsg, sessIdx, toolIdx int) {
		entry.content = stabilizeWidth(entry.content)
		prev := b.Len()
		idx := len(entries)
		content, lp, bw := cv.renderMessageBox(&b, entry, lay, idx == cv.selectedIdx)
		addEntry(entry, sessIdx, toolIdx, lp, bw, strings.Count(b.String()[prev:], "\n"), content)
		lastBoxIdx = idx
	}

	lastDivider := len(cv.compactSeqs) - 1
	for _, item := range groupMessages(msgs, cv.compactSeqs) {
		switch item.kind {
		case riCompactDivider:
			sep()
			divIdx := item.divIdx
			if divIdx == lastDivider && cv.compactExpanded[divIdx] && cv.compactSummary != "" {
				renderBox(renderMsg{
					role:      session.RoleAssistant,
					boxTitle:  fmt.Sprintf("[%s]compacted conversation[-]", TC.Muted),
					content:   cv.compactSummary,
					fullWidth: true,
				}, divIdx, toolIdxCompact)
			} else {
				label := "compacted conversation"
				labelW := len(label)               // ASCII only
				dashTotal := lay.band - labelW - 2 // spaces around label
				leftN := max(1, dashTotal/2)
				rightN := max(1, dashTotal-leftN)
				line := fmt.Sprintf("%s[%s]%s %s %s[-]", strings.Repeat(" ", lay.off), TC.Muted,
					strings.Repeat("─", leftN), label, strings.Repeat("─", rightN))
				b.WriteString(line)
				b.WriteString("\n")
				addEntry(renderMsg{}, divIdx, toolIdxCompact, lay.off, lay.band, 1, nil)
			}

		case riCollapsedRounds:
			sep()
			desc, thinkSecs := collapseStatuses(item.statuses)
			if !cv.statusExpanded[item.firstStatusIdx] {
				writeFlatLine(apexLabel(desc), item.firstStatusIdx, -1)
				break
			}
			title := fmt.Sprintf("[%s]apex[-][%s] (thoughts)[-]", TC.ApexColor, TC.Muted)
			if thinkSecs > 0 {
				title = fmt.Sprintf("[%s]apex[-][%s] (thoughts, %ds)[-]", TC.ApexColor, TC.Muted, thinkSecs)
			}
			renderBox(renderMsg{
				role: session.RoleAssistant, expandedBox: true,
				boxTitle: title, content: collapsedDetail(item.statuses, lay.asstW-4), contentTagged: true,
			}, item.firstStatusIdx, -1)

		case riLiveStatus:
			content := item.liveContent
			if content == "" {
				content = cv.liveStatus
			}
			if content == "" {
				continue
			}
			sep()
			// An in-progress status is expandable too: reveal its streaming
			// thinking text / running tool code, mirroring a settled round.
			if cv.statusExpanded[item.liveMsgIdx] {
				if detail := collapsedDetail([]session.Message{msgs[item.liveMsgIdx]}, lay.asstW-4); detail != "" {
					title := fmt.Sprintf("[%s]apex[-][%s] (%s)[-]", TC.ApexColor, TC.Muted, stripTags(content))
					renderBox(renderMsg{
						role: session.RoleAssistant, expandedBox: true,
						boxTitle: title, content: detail, contentTagged: true,
					}, item.liveMsgIdx, -1)
					break
				}
			}
			if len(content) > 0 && content[0] != '[' {
				content = apexLabel(content)
			}
			writeFlatLine(content, item.liveMsgIdx, -1)

		case riMessage:
			sep()
			renderBox(renderMsg{
				role: item.msg.Role, content: item.msg.Content, err: item.msg.Error, partial: item.msg.Partial,
			}, item.msgIdx, -1)
		}
	}

	cv.entries = entries
	// While following, pin the selection to the last box so it renders with the
	// focus border. Applied on the next rebuild (Draw sees selectedIdx change).
	if cv.autoFollow && lastBoxIdx >= 0 {
		cv.selectedIdx = lastBoxIdx
	}
	cv.buildCopyMasks(entries)

	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

// buildCopyMasks projects the per-line copy masks and soft-wrap flags out of the
// rendered content lines captured by renderMessageBox — no re-wrapping or width
// re-derivation. copyColMask[docLine] holds the line's content-relative mask;
// drawPartialSel treats columns past len(mask) as non-copyable, so a mask shorter
// than the box's content width excludes trailing padding without extra bookkeeping.
func (cv *ChatView) buildCopyMasks(entries []renderedEntry) {
	cv.copyColMask = make(map[int][]bool)
	cv.softWrapLine = make(map[int]bool)

	for _, e := range entries {
		for j, rl := range e.contentLines {
			docLine := e.lineStart + 1 + j
			cv.copyColMask[docLine] = rl.copyMask
			if rl.softWrap {
				cv.softWrapLine[docLine] = true
			}
		}
	}
}

// ─── selection ───────────────────────────────────────────────────────────────

// HasSelection reports whether there is an active drag selection.
func (cv *ChatView) HasSelection() bool { return cv.selActive }

// ClearHover clears the hover highlight; called via SetMouseCapture when the
// mouse moves outside the chat view's rect (which the chat handler never sees).
func (cv *ChatView) ClearHover() {
	cv.hoverIdx = -1
	// While following, selectedIdx is the pinned last-message focus, not a click
	// selection, so leave it for buildText to keep tracking the latest box.
	if !cv.autoFollow {
		cv.selectedIdx = -1
	}
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
	var result strings.Builder
	lastContribDocLine := -1

	cv.iterPartialSel(func(docLine, colLo, colHi int, mask []bool) {
		msgIdx := cv.findMsgAt(docLine)
		if msgIdx < 0 || msgIdx >= len(cv.entries) {
			return
		}
		e := cv.entries[msgIdx]
		lineWithinBox, ok := e.contentLineAt(docLine)
		if !ok {
			return
		}
		// Copy plaintext comes from the same rendered line the display used:
		// strip its tview tags (which restores escaped brackets) and mask columns.
		line := stripTags(e.contentLines[lineWithinBox].text)
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
		e := cv.entries[i]
		msg := e.msg
		var content string
		switch msg.role {
		case session.RoleUser:
			content = msg.content
		case session.RoleAssistant:
			if msg.err != nil {
				content = msg.err.Error()
			} else {
				content = msg.content
			}
		}
		if content == "" {
			continue
		}
		if multi {
			role := "assistant"
			if msg.role == session.RoleUser {
				role = "you"
			} else if msg.expandedBox {
				role = "thoughts"
			} else if e.toolIdx == toolIdxCompact {
				role = "compact"
			}
			parts = append(parts, role+":\n"+content)
		} else {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}
