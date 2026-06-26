package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksana/hypane/internal/session"
)

// tagRe matches tview color/style tags so we can measure visible width.
var tagRe = regexp.MustCompile(`\[[^\[]*\]`)

// ChatView displays the conversation history for a session.
type ChatView struct {
	*tview.TextView
	messages   []session.Message
	lastWidth  int
	TotalLines int // number of rendered lines, read by Scrollbar
}

// NewChatView creates the scrollable message display.
func NewChatView() *ChatView {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetScrollable(true)
	tv.SetWordWrap(true)
	tv.SetBorder(true)
	tv.SetTitle(" conversation ")
	tv.SetTitleColor(Theme.Header)
	tv.SetBorderColor(Theme.Border)
	tv.SetBackgroundColor(Theme.Background)

	return &ChatView{TextView: tv}
}

// SetFocused updates the border color to show keyboard focus.
func (cv *ChatView) SetFocused(focused bool) {
	if focused {
		cv.SetBorderColor(Theme.BorderFocus)
	} else {
		cv.SetBorderColor(Theme.Border)
	}
}

// Draw overrides tview's draw to rebuild text whenever the width changes.
func (cv *ChatView) Draw(screen tcell.Screen) {
	_, _, w, _ := cv.GetInnerRect()
	if w > 0 && w != cv.lastWidth {
		cv.lastWidth = w
		cv.buildText(w)
	}
	cv.TextView.Draw(screen)
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

func (cv *ChatView) buildText(width int) {
	var b strings.Builder
	first := true
	for _, msg := range cv.messages {
		if msg.Role == session.RoleTool {
			continue
		}
		if !first {
			b.WriteString("\n")
		}
		first = false
		renderMessage(&b, msg, width)
	}
	text := b.String()
	cv.TotalLines = strings.Count(text, "\n") + 1
	cv.TextView.SetText(text)
}

func renderMessage(b *strings.Builder, msg session.Message, width int) {
	// Each side occupies at most 4/5 of the available width.
	maxW := width * 4 / 5

	switch msg.Role {
	case session.RoleUser:
		label := fmt.Sprintf("[%s]you[-]", tviewColor(Theme.UserColor))
		fmt.Fprintln(b, rightAlign(label, width-2))
		for _, line := range wordWrap(msg.Content, maxW) {
			fmt.Fprintln(b, rightAlign(tview.Escape(line), width-2))
		}

	case session.RoleAssistant:
		if msg.Error != nil {
			fmt.Fprintf(b, "[%s]error: %s[-]\n",
				tviewColor(Theme.ErrorColor), tview.Escape(msg.Error.Error()))
			return
		}
		label := fmt.Sprintf("[%s]assistant[-]", tviewColor(Theme.AssistantColor))
		if msg.Partial {
			label += fmt.Sprintf(" [%s]…[-]", tviewColor(Theme.Muted))
		}
		fmt.Fprintf(b, "  %s\n", label)
		for _, line := range wordWrap(msg.Content, maxW-2) {
			fmt.Fprintf(b, "  %s\n", tview.Escape(line))
		}
		for _, tu := range msg.ToolUses {
			renderToolUse(b, tu)
		}
	}
}

func renderToolUse(b *strings.Builder, tu session.ToolUse) {
	arg := formatInput(tu.Input)
	switch tu.State {
	case "running":
		fmt.Fprintf(b, "  [%s]▶ %s[-][%s]%s[-] [%s]…[-]\n",
			tviewColor(Theme.ToolColor), tview.Escape(tu.Name),
			tviewColor(Theme.Muted), arg,
			tviewColor(Theme.Muted))
	case "done":
		fmt.Fprintf(b, "  [%s]▶ %s[-][%s]%s[-] [%s]✓[-]\n",
			tviewColor(Theme.ToolColor), tview.Escape(tu.Name),
			tviewColor(Theme.Muted), arg,
			tviewColor(Theme.SuccessColor))
	case "error":
		fmt.Fprintf(b, "  [%s]▶ %s[-][%s]%s[-] [%s]✗ %s[-]\n",
			tviewColor(Theme.ToolColor), tview.Escape(tu.Name),
			tviewColor(Theme.Muted), arg,
			tviewColor(Theme.ErrorColor), tview.Escape(tu.Output))
	default:
		fmt.Fprintf(b, "  [%s]▷ %s[-][%s]%s[-]\n",
			tviewColor(Theme.ToolColor), tview.Escape(tu.Name),
			tviewColor(Theme.Muted), arg)
	}
}

// wordWrap splits text into lines of at most maxW runes, breaking on word boundaries.
func wordWrap(text string, maxW int) []string {
	if maxW <= 0 {
		maxW = 40
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		if len([]rune(para)) <= maxW {
			out = append(out, para)
			continue
		}
		words := strings.Fields(para)
		cur := ""
		for _, w := range words {
			wlen := len([]rune(w))
			if cur == "" {
				cur = w
			} else if len([]rune(cur))+1+wlen <= maxW {
				cur += " " + w
			} else {
				out = append(out, cur)
				cur = w
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

// rightAlign pads s so its visible right edge sits at column width.
func rightAlign(s string, width int) string {
	pad := width - visibleLen(s)
	if pad <= 0 {
		return s
	}
	return strings.Repeat(" ", pad) + s
}

// visibleLen returns the display width of s after stripping tview color tags.
func visibleLen(s string) int {
	return len([]rune(tagRe.ReplaceAllString(s, "")))
}

func formatInput(input string) string {
	if input == "" || input == "{}" {
		return "()"
	}
	input = strings.TrimSpace(input)
	if len(input) > 50 {
		input = input[:47] + "..."
	}
	return "(" + input + ")"
}
