package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksana/hypane/internal/session"
)


// ChatView displays the conversation as individually bordered message boxes.
type ChatView struct {
	*tview.TextView
	messages     []session.Message
	lastWidth    int
	TotalLines   int // read by Scrollbar
	hoverIdx     int // index into renderedMsgs; -1 = none
	renderedMsgs []session.Message
	msgStartLine []int // document line where each renderedMsg's top border starts
}

// NewChatView creates the scrollable message display.
func NewChatView() *ChatView {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(false) // we manage layout manually
	tv.SetBorder(false)   // no outer frame; messages have their own boxes
	tv.SetBackgroundColor(Theme.Background)
	return &ChatView{TextView: tv, hoverIdx: -1}
}

// SetFocused is called by focus/blur hooks; no visible border to update.
func (cv *ChatView) SetFocused(_ bool) {}

// Draw rebuilds text on resize, renders via tview, then draws the hover overlay.
func (cv *ChatView) Draw(screen tcell.Screen) {
	_, _, w, _ := cv.GetInnerRect()
	if w > 0 && w != cv.lastWidth {
		cv.lastWidth = w
		cv.buildText(w)
	}
	cv.TextView.Draw(screen)
	cv.drawHoverOverlay(screen)
}

// drawHoverOverlay recolors box-border characters for the hovered message.
func (cv *ChatView) drawHoverOverlay(screen tcell.Screen) {
	if cv.hoverIdx < 0 || cv.hoverIdx >= len(cv.msgStartLine) {
		return
	}
	ix, iy, iw, ih := cv.GetInnerRect()
	scrollY, _ := cv.GetScrollOffset()

	startDoc := cv.msgStartLine[cv.hoverIdx]
	endDoc := cv.TotalLines
	if cv.hoverIdx+1 < len(cv.msgStartLine) {
		endDoc = cv.msgStartLine[cv.hoverIdx+1]
	}

	for doc := startDoc; doc < endDoc; doc++ {
		sy := iy + (doc - scrollY)
		if sy < iy || sy >= iy+ih {
			continue
		}
		for x := ix; x < ix+iw; x++ {
			r, comb, style, _ := screen.GetContent(x, sy)
			if isBoxBorderRune(r) {
				screen.SetContent(x, sy, r, comb, style.Foreground(Theme.BorderFocus))
			}
		}
	}
}

func isBoxBorderRune(r rune) bool {
	switch r {
	case '┌', '┐', '└', '┘', '─', '│':
		return true
	}
	return false
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

// MouseHandler wraps TextView's handler to update hover state on mouse move.
func (cv *ChatView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	orig := cv.TextView.MouseHandler()
	return func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		_, iy, _, _ := cv.GetInnerRect()
		scrollY, _ := cv.GetScrollOffset()
		_, my := event.Position()

		switch action {
		case tview.MouseMove, tview.MouseLeftClick:
			cv.hoverIdx = cv.findMsgAt((my - iy) + scrollY)
		}

		if orig != nil {
			return orig(action, event, setFocus)
		}
		return false, nil
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
		text := b.String()
		cv.TotalLines = strings.Count(text, "\n") + 1
		cv.TextView.SetText(text)
		return
	}

	var b strings.Builder
	var renderedMsgs []session.Message
	var msgStartLine []int

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
		renderMessageBox(&b, msg, width, maxW)
	}

	cv.renderedMsgs = renderedMsgs
	cv.msgStartLine = msgStartLine

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
func renderMessageBox(b *strings.Builder, msg session.Message, width, maxW int) {
	bc := tviewColor(Theme.Border)

	dash := func(n int) string {
		if n < 0 {
			n = 0
		}
		return strings.Repeat("─", n)
	}

	// boxLine renders one content row padded to fill contentW columns.
	mkBoxLine := func(contentW int) func(inner string, vlen int) string {
		return func(inner string, vlen int) string {
			pad := contentW - vlen
			if pad < 0 {
				pad = 0
			}
			return fmt.Sprintf("[%s]│[-] %s%s [%s]│[-]", bc, inner, strings.Repeat(" ", pad), bc)
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
		boxW := max(8, actualW+4)
		contentW := boxW - 4
		leftPad := width - boxW
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
		ac := tviewColor(Theme.AssistantColor)
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
			boxW := max(10, actualW+4)
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
		lines := wordWrap(msg.Content, maxContentW)
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
		// ┌─ assistant ┐ = 14 cols, ┌─ assistant … ┐ = 16 cols
		minBoxW := 14
		if msg.Partial {
			minBoxW = 16
		}
		boxW := max(minBoxW, actualW+4)
		contentW := boxW - 4
		boxLine := mkBoxLine(contentW)

		partialFrag := ""
		extraW := 0
		if msg.Partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", mc)
			extraW = 2
		}
		// ┌─ assistant ──...──┐  "─ assistant " = 12 visible cols
		b.WriteString(fmt.Sprintf("[%s]┌─ [%s]assistant [%s]%s%s┐[-]",
			bc, ac, bc, partialFrag, dash(boxW-14-extraW)) + "\n")
		for _, line := range lines {
			b.WriteString(boxLine(tview.Escape(line), tview.TaggedStringWidth(line)) + "\n")
		}
		for _, tu := range msg.ToolUses {
			inner, vlen := fmtToolUse(tu)
			b.WriteString(boxLine(inner, vlen) + "\n")
		}
		b.WriteString(fmt.Sprintf("[%s]└%s┘[-]", bc, dash(boxW-2)) + "\n")
	}
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

// ─── text helpers ─────────────────────────────────────────────────────────────

// wordWrap splits text into lines of at most maxW columns as measured by tview
// (uniseg grapheme clusters), breaking on word boundaries.
func wordWrap(text string, maxW int) []string {
	if maxW <= 0 {
		maxW = 40
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if tview.TaggedStringWidth(para) <= maxW {
			out = append(out, para)
			continue
		}
		words := strings.Fields(para)
		cur := ""
		curW := 0
		for _, w := range words {
			wW := tview.TaggedStringWidth(w)
			switch {
			case cur == "":
				cur, curW = w, wW
			case curW+1+wW <= maxW:
				cur += " " + w
				curW += 1 + wW
			default:
				out = append(out, cur)
				cur, curW = w, wW
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	if len(out) == 0 {
		out = []string{""}
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
