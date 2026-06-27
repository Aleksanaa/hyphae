package ui

import (
	"encoding/json"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ApprovalHeight is the fixed row count of the approval bar when visible.
const ApprovalHeight = 5

// ApprovalView is a fully custom-drawn confirmation bar for run_shell commands.
// It sits between the chat and input in the layout. When hidden its height is 0.
type ApprovalView struct {
	*tview.Box
	toolName  string
	argLabel  string
	argValue  string
	reasoning string
	selected  string // "allow" | "deny"
	denyText  string
	denyCursor int // rune index into denyText

	lastClickSide string
	lastClickTime time.Time

	visible bool
	onAllow func()
	onDeny  func(string)
}

func NewApprovalView() *ApprovalView {
	av := &ApprovalView{
		Box:      tview.NewBox(),
		selected: "allow",
	}
	return av
}

// ── public API ───────────────────────────────────────────────────────────────

func (av *ApprovalView) IsVisible() bool   { return av.visible }
func (av *ApprovalView) GetSelected() string { return av.selected }
func (av *ApprovalView) SetSelected(s string) { av.selected = s }

func (av *ApprovalView) Show(toolName, argsJSON, reasoning string) {
	av.toolName = toolName
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
	av.denyText = ""
	av.denyCursor = 0
	av.lastClickSide = ""
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
		av.Deny(av.denyText)
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

	pending := Theme.PendingColor
	borderSt := tcell.StyleDefault.Foreground(pending)
	bgSt := tcell.StyleDefault.Background(Theme.Surface)
	mutedSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(Theme.Surface)
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(Theme.Surface)
	shellSt := tcell.StyleDefault.Foreground(Theme.ShellColor).Background(Theme.Surface)

	// Fill all 5 rows with Surface background.
	for row := 0; row < ApprovalHeight; row++ {
		for col := 0; col < w; col++ {
			screen.SetContent(x+col, y+row, ' ', nil, bgSt)
		}
	}

	// ── top border: ┌─ run_shell ──...──┐ ─────────────────────────────────
	screen.SetContent(x, y, '┌', nil, borderSt)
	screen.SetContent(x+w-1, y, '┐', nil, borderSt)
	col := x + 1
	screen.SetContent(col, y, '─', nil, borderSt)
	col++
	screen.SetContent(col, y, ' ', nil, borderSt)
	col++
	toolSt := tcell.StyleDefault.Foreground(Theme.PendingColor)
	for _, r := range []rune(av.toolName) {
		screen.SetContent(col, y, r, nil, toolSt)
		col++
	}
	screen.SetContent(col, y, ' ', nil, borderSt)
	col++
	for ; col < x+w-1; col++ {
		screen.SetContent(col, y, '─', nil, borderSt)
	}

	// ── side borders (rows 1–3) ────────────────────────────────────────────
	for row := 1; row <= 3; row++ {
		screen.SetContent(x, y+row, '│', nil, borderSt)
		screen.SetContent(x+w-1, y+row, '│', nil, borderSt)
	}

	inner := x + 2             // content starts after "│ "
	innerW := w - 4            // width between "│ " and " │"

	// ── row 1: <label> ❯ <value> ──────────────────────────────────────────
	prefix := []rune(av.argLabel + " ❯ ")
	col = inner
	for _, r := range prefix {
		if col >= x+w-1 {
			break
		}
		screen.SetContent(col, y+1, r, nil, mutedSt)
		col++
	}
	maxCmd := innerW - len(prefix)
	for _, r := range []rune(truncateStr(av.argValue, maxCmd)) {
		if col >= x+w-1 {
			break
		}
		screen.SetContent(col, y+1, r, nil, shellSt)
		col++
	}

	// ── row 2: reason: <reasoning> ────────────────────────────────────────
	if av.reasoning != "" {
		reasonPrefix := []rune("reason: ")
		col = inner
		for _, r := range reasonPrefix {
			if col >= x+w-1 {
				break
			}
			screen.SetContent(col, y+2, r, nil, mutedSt)
			col++
		}
		maxReason := innerW - len(reasonPrefix)
		for _, r := range []rune(truncateStr(av.reasoning, maxReason)) {
			if col >= x+w-1 {
				break
			}
			screen.SetContent(col, y+2, r, nil, textSt)
			col++
		}
	}

	// ── row 3: [ Allow ]   [ Deny: <text>_ ] ──────────────────────────────
	allowLabel := []rune("[ Allow ]")
	allowLen := len(allowLabel) // 9
	white := tcell.NewRGBColor(240, 240, 240)
	darkGreen := tcell.NewRGBColor(30, 90, 50)
	darkRed := tcell.NewRGBColor(100, 35, 35)
	allowSt := tcell.StyleDefault.Foreground(Theme.SuccessColor).Background(Theme.Surface)
	if av.selected == "allow" {
		allowSt = tcell.StyleDefault.Background(darkGreen).Foreground(white)
	}
	col = inner
	for _, r := range allowLabel {
		screen.SetContent(col, y+3, r, nil, allowSt)
		col++
	}

	// gap
	col = inner + allowLen + 3

	// Deny area
	denyEnd := x + w - 2 // exclusive (before right margin space)
	denyW := denyEnd - col
	if denyW < 10 {
		goto bottomBorder
	}

	{
		denyBracketSt := tcell.StyleDefault.Foreground(Theme.ErrorColor).Background(Theme.Surface)
		denyTextSt := tcell.StyleDefault.Foreground(Theme.Text).Background(Theme.Surface)
		denyCursorSt := tcell.StyleDefault.Background(Theme.Text).Foreground(Theme.Surface)
		placeholderSt := mutedSt
		if av.selected == "deny" {
			denyBracketSt = tcell.StyleDefault.Background(darkRed).Foreground(white)
			denyTextSt = tcell.StyleDefault.Background(darkRed).Foreground(white)
			denyCursorSt = tcell.StyleDefault.Background(white).Foreground(darkRed)
			placeholderSt = tcell.StyleDefault.Background(darkRed).Foreground(Theme.Muted)
		}

		denyPrefix := []rune("[ Deny: ")
		denySuffix := []rune(" ]")
		textAreaW := denyW - len(denyPrefix) - len(denySuffix)
		if textAreaW < 0 {
			textAreaW = 0
		}

		for _, r := range denyPrefix {
			screen.SetContent(col, y+3, r, nil, denyBracketSt)
			col++
		}

		// Text area: show typed text, or greyed placeholder when empty.
		textRunes := []rune(av.denyText)
		placeholder := []rune("type reason here (optional)...")
		isEmpty := len(textRunes) == 0
		viewStart := 0
		if textAreaW > 0 && av.denyCursor >= textAreaW {
			viewStart = av.denyCursor - textAreaW + 1
		}
		for i := 0; i < textAreaW; i++ {
			runeIdx := viewStart + i
			var r rune = ' '
			var st tcell.Style
			if isEmpty {
				if runeIdx < len(placeholder) {
					r = placeholder[runeIdx]
				}
				st = placeholderSt
				if av.selected == "deny" && runeIdx == av.denyCursor {
					st = denyCursorSt
				}
			} else {
				if runeIdx < len(textRunes) {
					r = textRunes[runeIdx]
				}
				st = denyTextSt
				if av.selected == "deny" && runeIdx == av.denyCursor {
					st = denyCursorSt
				}
			}
			screen.SetContent(col, y+3, r, nil, st)
			col++
		}

		for _, r := range denySuffix {
			screen.SetContent(col, y+3, r, nil, denyBracketSt)
			col++
		}
	}

bottomBorder:
	// ── bottom border: └──...──┘ ──────────────────────────────────────────
	screen.SetContent(x, y+4, '└', nil, borderSt)
	screen.SetContent(x+w-1, y+4, '┘', nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, y+4, '─', nil, borderSt)
	}
}

// ── InputHandler ─────────────────────────────────────────────────────────────

func (av *ApprovalView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return av.WrapInputHandler(func(event *tcell.EventKey, _ func(tview.Primitive)) {
		if !av.visible {
			return
		}
		switch event.Key() {
		case tcell.KeyLeft:
			av.selected = "allow"
		case tcell.KeyRight:
			av.selected = "deny"
		case tcell.KeyEnter:
			av.confirm()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if av.selected == "deny" && av.denyCursor > 0 {
				r := []rune(av.denyText)
				av.denyText = string(append(r[:av.denyCursor-1], r[av.denyCursor:]...))
				av.denyCursor--
			}
		case tcell.KeyDelete:
			if av.selected == "deny" {
				r := []rune(av.denyText)
				if av.denyCursor < len(r) {
					av.denyText = string(append(r[:av.denyCursor], r[av.denyCursor+1:]...))
				}
			}
		default:
			if av.selected == "deny" && event.Rune() >= 32 {
				r := []rune(av.denyText)
				n := make([]rune, len(r)+1)
				copy(n, r[:av.denyCursor])
				n[av.denyCursor] = event.Rune()
				copy(n[av.denyCursor+1:], r[av.denyCursor:])
				av.denyText = string(n)
				av.denyCursor++
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

		// Only the action row (y+3) is interactive.
		if my != y+3 {
			return false, nil
		}

		inner := x + 2
		allowEnd := inner + 9 // "[ Allow ]"
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
			now := time.Now()
			if side == av.lastClickSide && now.Sub(av.lastClickTime) < 400*time.Millisecond {
				av.selected = side
				av.confirm()
			} else {
				av.selected = side
				av.lastClickSide = side
				av.lastClickTime = now
			}
			return true, nil
		}
		return false, nil
	})
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
