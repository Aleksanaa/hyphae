package ui

import (
	"encoding/json"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
)

// ApprovalHeight is the fixed row count of the approval bar when visible.
const ApprovalHeight = 5

var (
	approvalDarkGreen = tcell.NewRGBColor(30, 90, 50)
	approvalDarkRed   = tcell.NewRGBColor(100, 35, 35)
	approvalWhite     = tcell.NewRGBColor(240, 240, 240)
)

// ApprovalView is a confirmation bar for tool calls that don't produce a diff.
// It sits between the chat and input in the layout. When hidden its height is 0.
type ApprovalView struct {
	*tview.Box
	toolName  string
	argLabel  string
	argValue  string
	reasoning string
	selected  string // "allow" | "deny"

	// deny text is managed by a native InputField (handles cursor, CJK, wide chars).
	denyField *tview.InputField

	visible bool
	onAllow func()
	onDeny  func(string)
}

func NewApprovalView() *ApprovalView {
	av := &ApprovalView{
		Box:      tview.NewBox(),
		selected: "allow",
	}
	av.Box.SetBackgroundColor(Theme.Surface)
	av.SetBorder(true)
	av.SetBorderColor(Theme.PendingColor)
	av.SetTitleColor(Theme.PendingColor)
	av.SetTitleAlign(tview.AlignLeft)

	av.denyField = tview.NewInputField()
	av.denyField.SetPlaceholder("type reason here (optional)...")
	av.denyField.SetFieldTextColor(Theme.Text)
	av.denyField.SetFieldBackgroundColor(Theme.Surface)
	av.denyField.SetBackgroundColor(Theme.Surface)
	av.denyField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			av.confirm()
		}
	})

	return av
}

// ── Focus delegation ─────────────────────────────────────────────────────────

// Focus keeps hasFocus on the approval bar itself (so its InputHandler fires
// for Left/Right/Enter) and separately sets hasFocus on denyField (for cursor)
// when deny mode is active.
func (av *ApprovalView) Focus(delegate func(p tview.Primitive)) {
	av.Box.Focus(delegate) // sets av.Box.hasFocus = true
	if av.selected == "deny" {
		noop := func(tview.Primitive) {}
		av.denyField.Focus(noop) // sets denyField.Box.hasFocus for cursor
	} else {
		av.denyField.Blur()
	}
}

// ── public API ───────────────────────────────────────────────────────────────

func (av *ApprovalView) IsVisible() bool      { return av.visible }
func (av *ApprovalView) GetSelected() string  { return av.selected }
func (av *ApprovalView) SetSelected(s string) { av.selected = s }

func (av *ApprovalView) Show(toolName, argsJSON, reasoning string) {
	av.toolName = toolName
	av.SetTitle(" " + toolName + " ")
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err == nil {
		switch toolName {
		case "run_shell":
			av.argLabel = "command"
			if v, ok := args["command"].(string); ok {
				av.argValue = v
			} else {
				av.argValue = argsJSON
			}
		case "web_fetch":
			av.argLabel = "url"
			if v, ok := args["url"].(string); ok {
				av.argValue = v
			} else {
				av.argValue = argsJSON
			}
		case "web_search":
			av.argLabel = "query"
			if v, ok := args["query"].(string); ok {
				av.argValue = v
			} else {
				av.argValue = argsJSON
			}
		default:
			av.argLabel = "args"
			av.argValue = argsJSON
		}
	} else {
		av.argLabel = "args"
		av.argValue = argsJSON
	}
	av.reasoning = reasoning
	av.selected = "allow"
	av.denyField.SetText("")
	av.visible = true
}

func (av *ApprovalView) SetCallbacks(onAllow func(), onDeny func(string)) {
	av.onAllow = onAllow
	av.onDeny = onDeny
}

func (av *ApprovalView) Allow() {
	if av.onAllow != nil {
		av.onAllow()
	}
}

func (av *ApprovalView) Deny(reason string) {
	if av.onDeny != nil {
		av.onDeny(reason)
	}
}

func (av *ApprovalView) confirm() {
	if av.selected == "allow" {
		av.Allow()
	} else {
		av.Deny(av.denyField.GetText())
	}
}

// ── Draw ─────────────────────────────────────────────────────────────────────

func (av *ApprovalView) Draw(screen tcell.Screen) {
	av.Box.DrawForSubclass(screen, av)
	if !av.visible {
		return
	}
	x, y, w, h := av.GetRect()
	if w < 20 || h < ApprovalHeight {
		return
	}

	bg := Theme.Surface
	mutedSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(bg)
	shellSt := tcell.StyleDefault.Foreground(Theme.ShellColor).Background(bg)

	inner := x + 2
	innerW := w - 4

	// ── row 1: <label> ❯ <value> ──────────────────────────────────────────
	col := inner
	used := drawText(screen, av.argLabel+" ❯ ", col, y+1, innerW, mutedSt)
	col += used
	drawText(screen, truncateStr(av.argValue, innerW-used), col, y+1, innerW-used, shellSt)

	// ── row 2: reason: <reasoning> ────────────────────────────────────────
	if av.reasoning != "" {
		col = inner
		used = drawText(screen, "reason: ", col, y+2, innerW, mutedSt)
		col += used
		drawText(screen, truncateStr(av.reasoning, innerW-used), col, y+2, innerW-used, textSt)
	}

	// ── row 3: [ Allow ]   [ Deny: <denyField> ] ──────────────────────────
	allowSt := tcell.StyleDefault.Foreground(Theme.SuccessColor).Background(bg)
	if av.selected == "allow" {
		allowSt = tcell.StyleDefault.Background(approvalDarkGreen).Foreground(approvalWhite)
	}
	col = inner
	col += drawText(screen, "[ Allow ]", col, y+3, innerW, allowSt)

	// gap
	col = inner + 9 + 3

	denyEnd := x + w - 2
	denyW := denyEnd - col
	if denyW >= 10 {
		denyBracketSt := tcell.StyleDefault.Foreground(Theme.ErrorColor).Background(bg)
		if av.selected == "deny" {
			denyBracketSt = tcell.StyleDefault.Background(approvalDarkRed).Foreground(approvalWhite)
		}

		const denyPfx = "[ Deny: "
		const denySfx = " ]"
		col += drawText(screen, denyPfx, col, y+3, denyW, denyBracketSt)

		textAreaW := denyEnd - col - len([]rune(denySfx))
		if textAreaW > 0 {
			if av.selected == "deny" {
				av.denyField.SetFieldBackgroundColor(approvalDarkRed)
				av.denyField.SetFieldTextColor(approvalWhite)
				av.denyField.SetPlaceholderStyle(
					tcell.StyleDefault.Foreground(Theme.Muted).Background(approvalDarkRed))
			} else {
				av.denyField.SetFieldBackgroundColor(bg)
				av.denyField.SetFieldTextColor(Theme.Muted)
				av.denyField.SetPlaceholderStyle(
					tcell.StyleDefault.Foreground(Theme.Muted).Background(bg))
			}
			av.denyField.SetBackgroundColor(bg)
			av.denyField.SetRect(col, y+3, textAreaW, 1)
			av.denyField.Draw(screen)
			col += textAreaW
		}
		drawText(screen, denySfx, col, y+3, len([]rune(denySfx)), denyBracketSt)
	}

}

// ── InputHandler ─────────────────────────────────────────────────────────────

func (av *ApprovalView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return av.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		if !av.visible {
			return
		}
		switch event.Key() {
		case tcell.KeyLeft:
			av.selected = "allow"
			av.denyField.Blur()
			setFocus(av)
		case tcell.KeyRight:
			av.selected = "deny"
			av.denyField.Focus(func(tview.Primitive) {})
			setFocus(av)
		case tcell.KeyEnter:
			av.confirm()
		default:
			// Route all other input (runes, backspace, etc.) to denyField
			// when deny mode is active.
			if av.selected == "deny" {
				if h := av.denyField.InputHandler(); h != nil {
					h(event, setFocus)
				}
			}
		}
	})
}

// ── MouseHandler ─────────────────────────────────────────────────────────────

func (av *ApprovalView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return av.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !av.visible {
			return false, nil
		}
		mx, my := event.Position()
		x, y, w, _ := av.GetRect()

		if my != y+3 {
			return false, nil
		}

		inner := x + 2
		allowEnd := inner + 9
		denyStart := inner + 9 + 3
		denyEnd := x + w - 2

		var side string
		switch {
		case mx >= inner && mx < allowEnd:
			side = "allow"
		case mx >= denyStart && mx < denyEnd:
			side = "deny"
		default:
			return false, nil
		}

		switch action {
		case tview.MouseLeftDown:
			setFocus(av)
			return true, nil
		case tview.MouseLeftClick:
			av.selected = side
			setFocus(av)
			return true, nil
		case tview.MouseLeftDoubleClick:
			av.selected = side
			av.confirm()
			return true, nil
		}
		return false, nil
	})
}

func drawText(screen tcell.Screen, text string, col, row, maxCols int, st tcell.Style) int {
	used := 0
	for _, r := range text {
		rw := uniseg.StringWidth(string(r))
		if rw == 0 {
			rw = 1
		}
		if used+rw > maxCols {
			break
		}
		screen.SetContent(col+used, row, r, nil, st)
		used += rw
	}
	return used
}

func truncateStr(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
