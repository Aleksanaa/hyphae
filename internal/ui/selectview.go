package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

var selectHighlightBg = tcell.NewRGBColor(40, 60, 110)

// SelectView is a pick-one prompt shown when the agent calls ask_user.
// Height varies with option count; use SelectViewHeight to compute it.
type SelectView struct {
	*tview.Box
	question    string
	options     []string
	cursor      int // 0..len(options); len(options) == custom-text row
	customField *tview.InputField
	visible     bool
	onSubmit    func(string)
}

func NewSelectView() *SelectView {
	sv := &SelectView{Box: tview.NewBox()}
	sv.Box.SetBackgroundColor(Theme.Surface)
	sv.SetBorder(true)
	sv.SetBorderColor(Theme.PendingColor)
	sv.SetTitleColor(Theme.PendingColor)
	sv.SetTitleAlign(tview.AlignLeft)
	sv.SetTitle(" apex is asking ")

	sv.customField = tview.NewInputField()
	sv.customField.SetPlaceholder("tell apex what to do instead...")
	sv.customField.SetFieldTextColor(Theme.Text)
	sv.customField.SetFieldBackgroundColor(Theme.Surface)
	sv.customField.SetBackgroundColor(Theme.Surface)
	sv.customField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			sv.confirm()
		}
	})
	return sv
}

func (sv *SelectView) IsVisible() bool { return sv.visible }

// SelectViewHeight returns the row count for n model-provided options
// (the custom-text row is always present and is not included in n).
func SelectViewHeight(n int) int { return n + 5 }

func (sv *SelectView) Show(question string, options []string) {
	sv.question = question
	sv.options = options
	sv.cursor = 0
	sv.customField.SetText("")
	sv.visible = true
}

func (sv *SelectView) SetCallback(fn func(string)) { sv.onSubmit = fn }

func (sv *SelectView) confirm() {
	if !sv.visible || sv.onSubmit == nil {
		return
	}
	if sv.cursor < len(sv.options) {
		sv.onSubmit(sv.options[sv.cursor])
	} else if text := sv.customField.GetText(); text != "" {
		sv.onSubmit(text)
	}
}

// Cancel sends a sentinel reply so the agent is not left hanging.
func (sv *SelectView) Cancel() {
	if sv.onSubmit != nil {
		sv.onSubmit("(user dismissed without selecting)")
	}
}

// Focus delegates cursor focus to customField when it is the active row.
func (sv *SelectView) Focus(delegate func(p tview.Primitive)) {
	sv.Box.Focus(delegate)
	if sv.cursor == len(sv.options) {
		sv.customField.Focus(func(tview.Primitive) {})
	} else {
		sv.customField.Blur()
	}
}

// ── Draw ─────────────────────────────────────────────────────────────────────

func (sv *SelectView) Draw(screen tcell.Screen) {
	sv.Box.DrawForSubclass(screen, sv)
	if !sv.visible {
		return
	}
	x, y, w, h := sv.GetRect()
	if w < 20 || h < 4 {
		return
	}
	inner := x + 2
	innerW := w - 4
	bg := Theme.Surface

	mutedSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(bg)

	// Row 1: question text.
	drawText(screen, truncateStr(sv.question, innerW), inner, y+1, innerW, textSt)

	// Row 3+: option rows (y+2 is blank padding).
	total := len(sv.options) + 1 // includes custom-text row
	for i := 0; i < total && y+3+i < y+h-1; i++ {
		row := y + 3 + i
		isSelected := i == sv.cursor

		var st tcell.Style
		if isSelected {
			st = tcell.StyleDefault.Foreground(Theme.Text).Background(selectHighlightBg)
			for col := x + 1; col < x+w-1; col++ {
				screen.SetContent(col, row, ' ', nil, st)
			}
		} else {
			st = mutedSt
		}

		bullet := "○ "
		if isSelected {
			bullet = "● "
		}
		col := inner
		bulletW := drawText(screen, bullet, col, row, innerW, st)
		col += bulletW
		remaining := innerW - bulletW

		if i < len(sv.options) {
			// Option text is light even when not selected; only the bullet is muted.
			optSt := textSt
			if isSelected {
				optSt = st
			}
			drawText(screen, truncateStr(sv.options[i], remaining), col, row, remaining, optSt)
		} else {
			fieldW := remaining
			if fieldW > 0 {
				if isSelected {
					sv.customField.SetFieldBackgroundColor(selectHighlightBg)
					sv.customField.SetFieldTextColor(Theme.Text)
					sv.customField.SetPlaceholderStyle(
						tcell.StyleDefault.Foreground(Theme.Muted).Background(selectHighlightBg))
					sv.customField.SetBackgroundColor(selectHighlightBg)
				} else {
					sv.customField.SetFieldBackgroundColor(bg)
					sv.customField.SetFieldTextColor(Theme.Text) // typed text matches option brightness
					sv.customField.SetPlaceholderStyle(
						tcell.StyleDefault.Foreground(Theme.Muted).Background(bg))
					sv.customField.SetBackgroundColor(bg)
				}
				sv.customField.SetRect(col, row, fieldW, 1)
				sv.customField.Draw(screen)
			}
		}
	}
}

// ── InputHandler ─────────────────────────────────────────────────────────────

func (sv *SelectView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return sv.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		if !sv.visible {
			return
		}
		switch event.Key() {
		case tcell.KeyUp:
			if sv.cursor > 0 {
				sv.cursor--
			}
			if sv.cursor == len(sv.options) {
				sv.customField.Focus(func(tview.Primitive) {})
			} else {
				sv.customField.Blur()
			}
			setFocus(sv)
		case tcell.KeyDown:
			if sv.cursor < len(sv.options) {
				sv.cursor++
			}
			if sv.cursor == len(sv.options) {
				sv.customField.Focus(func(tview.Primitive) {})
			} else {
				sv.customField.Blur()
			}
			setFocus(sv)
		case tcell.KeyEnter:
			sv.confirm()
		default:
			// Forward all other input to customField when it is the active row.
			if sv.cursor == len(sv.options) {
				if h := sv.customField.InputHandler(); h != nil {
					h(event, setFocus)
				}
			}
		}
	})
}

// ── MouseHandler ─────────────────────────────────────────────────────────────

func (sv *SelectView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return sv.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !sv.visible {
			return false, nil
		}
		_, my := event.Position()
		_, y, _, _ := sv.GetRect()

		optRow := my - (y + 3)
		total := len(sv.options) + 1
		if optRow < 0 || optRow >= total {
			return false, nil
		}

		switch action {
		case tview.MouseLeftDown:
			setFocus(sv)
			return true, nil
		case tview.MouseLeftClick:
			sv.cursor = optRow
			if sv.cursor == len(sv.options) {
				sv.customField.Focus(func(tview.Primitive) {})
			} else {
				sv.customField.Blur()
			}
			setFocus(sv)
			return true, nil
		case tview.MouseLeftDoubleClick:
			sv.cursor = optRow
			if sv.cursor == len(sv.options) {
				sv.customField.Focus(func(tview.Primitive) {})
			}
			sv.confirm()
			return true, nil
		}
		return false, nil
	})
}
