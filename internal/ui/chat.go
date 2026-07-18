package ui

import (
	"encoding/json"
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

// renderMsg is the UI's view of one rendered entry, decoupled from session.Message
// (which no longer carries box-styling fields). role reuses session.Role values;
// a status role (RoleThinking/RoleTool) marks a flat status line, while expandedBox
// marks a status box. This keeps the box renderer and selection code role-driven.
type renderMsg struct {
	role          session.Role
	content       string
	err           error
	partial       bool
	expandedBox   bool
	boxTitle      string
	fullWidth     bool
	contentTagged bool
}

// renderedEntry records one displayed item with its session origin and screen geometry.
type renderedEntry struct {
	msg       renderMsg
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

// Restyle re-applies theme colors after a theme switch.
func (cv *ChatView) Restyle() {
	cv.SetBackgroundColor(Theme.Background)
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

// ─── message grouping ────────────────────────────────────────────────────────

type renderItemKind int

const (
	riCompactDivider  renderItemKind = iota // compact separator line or summary box
	riCollapsedRounds                       // ≥1 consecutive settled status items collapsed into one apex entry
	riLiveStatus                            // in-progress status (streaming thinking / running tool)
	riMessage                               // user or assistant message with content or error
)

// renderItem is one logical unit to be rendered in the conversation.
type renderItem struct {
	kind renderItemKind

	divIdx int // riCompactDivider: index into compactSeqs

	// riCollapsedRounds: the run of consecutive settled status items.
	firstStatusIdx int
	statuses       []session.Message

	// riLiveStatus
	liveMsgIdx  int
	liveContent string // from the status's own events/secs; liveStatus fallback applied in buildText

	// riMessage
	msg    session.Message
	msgIdx int
}

// statusInProgress reports whether a status item is still live: thinking that is
// streaming, or a tool call awaiting approval or running.
func statusInProgress(m session.Message) bool {
	if m.Partial {
		return true
	}
	for _, tu := range m.ToolUses {
		if tu.State == "running" || tu.State == "pending" {
			return true
		}
	}
	return false
}

// formatStatusEvent renders a single op event as live progress text.
func formatStatusEvent(ev session.StatusEvent) string {
	switch ev.Kind {
	case session.StatusEventDoing:
		return ev.Verb + " " + ev.Target
	case session.StatusEventWants:
		return "wants to " + ev.Verb + " " + ev.Target
	case session.StatusEventDone:
		return ev.Verb + " " + ev.Target
	case session.StatusEventRefused:
		return "wanted to " + ev.Verb + " " + ev.Target
	case session.StatusEventFailed:
		return "failed to " + ev.Verb + " " + ev.Target
	}
	return ""
}

// liveStatusText builds the progress line for an in-progress status from its own
// thinking duration and latest op event. Empty when there is nothing yet — the
// caller falls back to the transient liveStatus label.
func liveStatusText(m session.Message) string {
	var thinkText string
	if m.Role == session.RoleThinking && m.ThinkingSecs > 0 {
		thinkText = fmt.Sprintf("thought for %ds", m.ThinkingSecs)
	}
	var opsText string
	for _, ev := range m.StatusEvents {
		opsText = formatStatusEvent(ev)
	}
	switch {
	case thinkText != "" && opsText != "":
		return thinkText + ", " + opsText
	case thinkText != "":
		return thinkText
	default:
		return opsText
	}
}

// toolCode extracts the Starlark code and output from a tool status item.
func toolCode(m session.Message) (code, output string) {
	if len(m.ToolUses) == 0 {
		return "", ""
	}
	var args struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(m.ToolUses[0].Input), &args) == nil {
		code = args.Code
	}
	return code, m.ToolUses[0].Output
}

// collapseStatuses summarizes a run of settled status items into a one-line
// description ("thought for 3s, read 2 files") and the total thinking duration
// used for the expanded-box title.
func collapseStatuses(statuses []session.Message) (desc string, thinkSecs int) {
	anyThinking := false
	var allEvents []session.StatusEvent
	for _, s := range statuses {
		if s.Role == session.RoleThinking {
			thinkSecs += s.ThinkingSecs
			if s.Thinking != "" || s.ThinkingSecs > 0 {
				anyThinking = true
			}
		} else {
			allEvents = append(allEvents, s.StatusEvents...)
		}
	}
	agg := aggregateOps(allEvents)
	if !anyThinking {
		return agg, 0
	}
	if thinkSecs > 0 {
		desc = fmt.Sprintf("thought for %ds", thinkSecs)
	} else {
		desc = "thought for a moment"
	}
	if agg != "" {
		desc += ", " + agg
	}
	return desc, thinkSecs
}

// collapsedDetail renders the expanded-box body for a run of statuses: each
// thinking status's text and each tool status's code/output box, blank-separated.
func collapsedDetail(statuses []session.Message, contentW int) string {
	var lines []string
	firstBlock := true
	blank := func() {
		if !firstBlock {
			lines = append(lines, "")
		}
		firstBlock = false
	}
	for _, s := range statuses {
		if s.Role == session.RoleThinking {
			if s.Thinking != "" {
				blank()
				lines = append(lines, tview.Escape(s.Thinking))
			}
		} else if code, output := toolCode(s); code != "" {
			blank()
			for _, rl := range renderCodeOutputLines(code, output, contentW) {
				lines = append(lines, rl.text)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// groupMessages converts the flat message list into ordered render items,
// interleaving compact dividers and collapsing consecutive settled status
// (thinking/tool) items into one apex entry. Because each round is already its
// own peer item, this is a single forward scan — no re-pairing of roles.
func groupMessages(msgs []session.Message, compactSeqs []int) []renderItem {
	var items []renderItem
	mn := len(msgs)
	nextDivider := 0

	flushDividers := func(before int) {
		for nextDivider < len(compactSeqs) && before > compactSeqs[nextDivider] {
			items = append(items, renderItem{kind: riCompactDivider, divIdx: nextDivider})
			nextDivider++
		}
	}

	for mi := 0; mi < mn; mi++ {
		flushDividers(mi)
		msg := msgs[mi]

		if msg.Role.IsStatus() {
			// An in-progress status renders live, not collapsed.
			if statusInProgress(msg) {
				items = append(items, renderItem{
					kind:        riLiveStatus,
					liveMsgIdx:  mi,
					liveContent: liveStatusText(msg),
				})
				continue
			}
			// Collect the run of consecutive settled status items.
			start := mi
			var statuses []session.Message
			for mi < mn && msgs[mi].Role.IsStatus() && !statusInProgress(msgs[mi]) {
				statuses = append(statuses, msgs[mi])
				mi++
			}
			mi-- // outer loop will re-increment
			items = append(items, renderItem{
				kind:           riCollapsedRounds,
				firstStatusIdx: start,
				statuses:       statuses,
			})
			continue
		}

		if msg.Role == session.RoleUser || msg.Content != "" || msg.Error != nil {
			items = append(items, renderItem{kind: riMessage, msg: msg, msgIdx: mi})
		}
	}

	// Flush any remaining dividers (e.g. compact just happened with no new messages yet).
	for nextDivider < len(compactSeqs) {
		items = append(items, renderItem{kind: riCompactDivider, divIdx: nextDivider})
		nextDivider++
	}
	return items
}

// aggregateOps groups Done and Failed status events by (kind, verb, noun), sums counts,
// and formats a summary. e.g. "read 2 files, failed to read 1 file, ran 3 functions"
func aggregateOps(events []session.StatusEvent) string {
	type key struct {
		kind        session.StatusEventKind
		verb, nounP string
	}
	type group struct {
		count int
		last  string
	}
	var order []key
	groups := map[key]*group{}
	for _, ev := range events {
		if ev.Kind != session.StatusEventDone && ev.Kind != session.StatusEventFailed {
			continue
		}
		k := key{ev.Kind, ev.Verb, ev.NounP}
		if g, ok := groups[k]; ok {
			g.count++
			g.last = ev.Target
		} else {
			order = append(order, k)
			groups[k] = &group{1, ev.Target}
		}
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i, k := range order {
		g := groups[k]
		prefix := ""
		if k.kind == session.StatusEventFailed {
			prefix = "failed to "
		}
		if g.count == 1 {
			parts[i] = prefix + k.verb + " " + g.last
		} else {
			parts[i] = fmt.Sprintf("%s%s %d %s", prefix, k.verb, g.count, k.nounP)
		}
	}
	return strings.Join(parts, ", ")
}

// renderCodeOutputLines produces a bordered box containing syntax-highlighted
// Starlark code and its output for embedding inside an ExpandedBox.
// maxW is the inner content width of the containing ExpandedBox.
// Code is capped at 20 lines; output at 10 lines.
func renderCodeOutputLines(code, output string, maxW int) []renderedLine {
	const codeMaxLines = 20
	const outputMaxLines = 10

	bc := TC.Border
	mc := TC.Muted

	innerW := max(1, maxW-4)
	fill := func(n int) string { return strings.Repeat("─", max(0, n)) }

	wrapLine := func(rl renderedLine) renderedLine {
		visW := tview.TaggedStringWidth(rl.text)
		pad := max(0, innerW-visW)
		text := fmt.Sprintf("[%s]│[-:-:-] %s%s [%s]│[-:-:-]", bc, rl.text, strings.Repeat(" ", pad), bc)
		totalW := 2 + visW + pad + 2
		mask := make([]bool, totalW)
		for col := 2; col < 2+visW; col++ {
			mask[col] = true
		}
		return renderedLine{text: text, copyMask: mask, softWrap: rl.softWrap}
	}

	var out []renderedLine

	// Top border: ┌──────────────────── input ─┐ (right-aligned)
	{
		inLabel := "input"
		leftFill := max(0, maxW-len([]rune(inLabel))-5)
		top := fmt.Sprintf("[%s]┌%s [%s]%s [%s]─┐[-:-:-]", bc, fill(leftFill), mc, inLabel, bc)
		out = append(out, renderedLine{text: top, copyMask: make([]bool, tview.TaggedStringWidth(top))})
	}

	// Code lines with python highlighting
	allCodeLines := strings.Split(code, "\n")
	truncatedCode := len(allCodeLines) > codeMaxLines
	codeLines := allCodeLines
	if truncatedCode {
		codeLines = allCodeLines[:codeMaxLines]
	}
	cb := &codeBlock{lang: "python", lines: codeLines}
	for _, rl := range cb.renderHighlighted(innerW) {
		out = append(out, wrapLine(rl))
	}
	if truncatedCode {
		trunc := fmt.Sprintf("[%s]… %d more lines[-:-:-]", mc, len(allCodeLines)-codeMaxLines)
		out = append(out, wrapLine(renderedLine{text: trunc}))
	}

	// Output section (omit trivial placeholder)
	if output != "" && output != "(done)" {
		// Middle separator: ├──────────────────── output ─┤ (right-aligned)
		outLabel := "output"
		leftFill := max(0, maxW-len([]rune(outLabel))-5)
		mid := fmt.Sprintf("[%s]├%s [%s]%s [%s]─┤[-:-:-]", bc, fill(leftFill), mc, outLabel, bc)
		out = append(out, renderedLine{text: mid, copyMask: make([]bool, tview.TaggedStringWidth(mid))})

		allOutLines := strings.Split(output, "\n")
		truncatedOut := len(allOutLines) > outputMaxLines
		outLines := allOutLines
		if truncatedOut {
			outLines = allOutLines[:outputMaxLines]
		}
		cb2 := &codeBlock{lines: outLines}
		for _, rl := range cb2.renderPlain(innerW) {
			out = append(out, wrapLine(rl))
		}
		if truncatedOut {
			trunc := fmt.Sprintf("[%s]… %d more lines[-:-:-]", mc, len(allOutLines)-outputMaxLines)
			out = append(out, wrapLine(renderedLine{text: trunc}))
		}
	}

	// Bottom border: └────────────────────────────────────────────┘
	bot := fmt.Sprintf("[%s]└%s┘[-:-:-]", bc, fill(maxW-2))
	out = append(out, renderedLine{text: bot, copyMask: make([]bool, tview.TaggedStringWidth(bot))})

	return out
}

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

	addEntry := func(entry renderMsg, sessIdx, toolIdx, lp, bw, lines int) {
		entries = append(entries, renderedEntry{
			msg: entry, sessIdx: sessIdx, toolIdx: toolIdx,
			lineStart: lineCount, boxLeft: lp, boxRight: lp + bw,
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
		b.WriteString(strings.Repeat(" ", lay.off+2))
		b.WriteString(line)
		b.WriteString("\n")
		addEntry(renderMsg{role: session.RoleTool, content: line}, sessIdx, toolIdx, lay.off+2, tview.TaggedStringWidth(line), 1)
	}
	renderBox := func(entry renderMsg, sessIdx, toolIdx int) {
		prev := b.Len()
		idx := len(entries)
		lp, bw := cv.renderMessageBox(&b, entry, lay, idx == cv.selectedIdx)
		addEntry(entry, sessIdx, toolIdx, lp, bw, strings.Count(b.String()[prev:], "\n"))
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
				addEntry(renderMsg{}, divIdx, toolIdxCompact, lay.off, lay.band, 1)
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
	cv.buildCopyMasks(entries, lay)

	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

// buildCopyMasks populates copyColMask and softWrapLine for all rendered messages.
// copyColMask[line] is absent when the line is fully copyable; present with a
// []bool mask when trailing padding or format chars must be excluded from copies.
func (cv *ChatView) buildCopyMasks(entries []renderedEntry, lay convoLayout) {
	cv.copyColMask = make(map[int][]bool)
	cv.softWrapLine = make(map[int]bool)

	maskWordWrap := func(start, minCW, cw int, content string) {
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
		asstCW := lay.asstW - 4
		switch msg.role {
		case session.RoleUser:
			maskWordWrap(start, 4, lay.userW-4, tview.Escape(msg.content))
		case session.RoleAssistant:
			switch {
			case msg.expandedBox:
				content := msg.content
				if !msg.contentTagged {
					content = tview.Escape(content)
				}
				maskWordWrap(start, 1, asstCW, content)
			case msg.err != nil:
				maskWordWrap(start, 6, asstCW, tview.Escape(msg.err.Error()))
			case msg.content != "":
				blocks := cv.mdCache[msg.content]
				if blocks == nil {
					break
				}
				contentCW := asstCW
				if msg.fullWidth {
					contentCW = (e.boxRight - e.boxLeft) - 4
				}
				rls := renderBlocksAnnotated(blocks, contentCW)
				actualW := 0
				for _, rl := range rls {
					if n := len(rl.copyMask); n > actualW {
						actualW = n
					}
				}
				minBW := 9
				if msg.partial {
					minBW = 11
				} else if msg.boxTitle != "" {
					minBW = tview.TaggedStringWidth(msg.boxTitle) + 5
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
// expandedBox uses dotted borders (╌/╎) with boxTitle; boxTitle alone uses solid borders
// with a centered title; otherwise solid borders with the "apex" label.
func (cv *ChatView) renderMessageBox(b *strings.Builder, msg renderMsg, lay convoLayout, isHovered bool) (leftPad, boxW int) {
	bc := Theme.Border.CSS()
	if isHovered {
		bc = Theme.BorderFocus.CSS()
	}

	hChar, vChar := "─", "│"
	if msg.expandedBox {
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

	switch msg.role {
	case session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.content), lay.userW-4)
		boxW = max(8, maxTaggedWidth(lines)+4)
		// Right-align within the band; the capped width leaves ≥20% blank on the band's left.
		leftPad = lay.off + max(0, lay.band-boxW)
		p := strings.Repeat(" ", leftPad)
		boxLine := mkLine(boxW - 4)
		fmt.Fprintf(b, "%s[%s]┌─ [%s]you [%s]%s┐[-]\n", p, bc, TC.UserColor, bc, fill(boxW-8))
		for _, line := range lines {
			fmt.Fprintf(b, "%s%s\n", p, boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))

	case session.RoleAssistant:
		// Assistant boxes hug the band's left edge; their capped width leaves the right margin.
		maxContentW := lay.asstW - 4
		leftPad = lay.off
		p := strings.Repeat(" ", lay.off)
		if msg.err != nil {
			lines := tview.WordWrap(tview.Escape(msg.err.Error()), maxContentW)
			boxW = max(10, maxTaggedWidth(lines)+4)
			boxLine := mkLine(boxW - 4)
			fmt.Fprintf(b, "%s[%s]┌─ [%s]error [%s]%s┐[-]\n", p, bc, TC.ErrorColor, bc, fill(boxW-10))
			for _, line := range lines {
				fmt.Fprintf(b, "%s%s\n", p, boxLine(fmt.Sprintf("[%s]%s[-]", TC.ErrorColor, line), tview.TaggedStringWidth(line)))
			}
			fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))
			return
		}

		var lines []string
		if msg.expandedBox {
			content := msg.content
			if !msg.contentTagged {
				content = tview.Escape(content)
			}
			lines = tview.WordWrap(content, maxContentW)
		} else {
			blocks, ok := cv.mdCache[msg.content]
			if !ok {
				blocks = parseMarkdown(msg.content)
				cv.mdCache[msg.content] = blocks
			}
			lines = renderBlocks(blocks, maxContentW)
		}

		partialFrag, extraW := "", 0
		if msg.partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", TC.Muted)
			extraW = 2
		}
		minBoxW := 9
		if msg.boxTitle != "" {
			minBoxW = tview.TaggedStringWidth(msg.boxTitle) + 5
		} else if msg.partial {
			minBoxW = 11
		}
		if msg.fullWidth {
			boxW = lay.asstW
		} else {
			boxW = max(minBoxW+extraW, maxTaggedWidth(lines)+4)
		}
		boxLine := mkLine(boxW - 4)

		if msg.expandedBox {
			fmt.Fprintf(b, "%s[%s]┌╌ %s %s[%s]%s┐[-]\n",
				p, bc, msg.boxTitle, partialFrag, bc, fill(boxW-tview.TaggedStringWidth(msg.boxTitle)-5-extraW))
			if len(lines) == 0 {
				fmt.Fprintf(b, "%s%s\n", p, boxLine("", 0))
			}
		} else if msg.boxTitle != "" {
			titleTagW := tview.TaggedStringWidth(msg.boxTitle)
			inner := boxW - titleTagW - 4 // total dash space
			leftF := inner / 2
			rightF := inner - leftF
			fmt.Fprintf(b, "%s[%s]┌%s %s [%s]%s┐[-]\n",
				p, bc, fill(leftF), msg.boxTitle, bc, fill(rightF))
		} else {
			fmt.Fprintf(b, "%s[%s]┌─ [%s]apex [%s]%s[%s]%s┐[-]\n",
				p, bc, TC.ApexColor, bc, partialFrag, bc, fill(boxW-9-extraW))
		}
		for _, line := range lines {
			fmt.Fprintf(b, "%s%s\n", p, boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))
	}
	return
}

// apexLabel wraps desc in the dim "apex" prefix with muted text color.
func apexLabel(desc string) string {
	return fmt.Sprintf("[%s]apex[-][%s] %s[-]", TC.ApexDim, TC.Muted, desc)
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
	lay := newConvoLayout(cv.lastWidth)

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
			allLines, _ = computeMsgContent(cv.entries[msgIdx].msg, lay)
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

// computeMsgContent returns the plain-text content lines for a rendered entry,
// wrapped to the same per-role column width used when rendering (asstW for
// assistant/status content, userW for user messages). The second return is a
// legacy indent hint kept for callers that still read it.
func computeMsgContent(msg renderMsg, lay convoLayout) ([]string, int) {
	switch {
	case msg.role == session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.content), lay.userW-4)
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, 0
	case msg.role == session.RoleAssistant:
		cw := lay.asstW - 4
		var plain string
		switch {
		case msg.expandedBox:
			plain = msg.content
		case msg.err != nil:
			plain = msg.err.Error()
		default:
			raw := renderMarkdown(msg.content, cw)
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
	case msg.role.IsStatus():
		return []string{stripTags(msg.content)}, 2
	}
	return nil, 0
}
