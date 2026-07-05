package ui

import (
	"strings"

	"github.com/atotto/clipboard"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// InputView wraps a TextArea to provide the user message input area.
type InputView struct {
	*tview.TextArea
	onSend func(text string)
}

// NewInputView creates the message input area.
func NewInputView(onSend func(string)) *InputView {
	ta := tview.NewTextArea()
	ta.SetBorder(true)
	ta.SetTitle(" message ")
	ta.SetTitleColor(Theme.Header)
	ta.SetBorderColor(Theme.Border)
	ta.SetBackgroundColor(Theme.Surface)
	ta.SetTextStyle(tcell.StyleDefault.
		Foreground(Theme.Text).
		Background(Theme.Surface))
	ta.SetPlaceholder("Type a message…  (Ctrl+S to send)")
	ta.SetPlaceholderStyle(tcell.StyleDefault.
		Foreground(Theme.Muted).
		Background(Theme.Surface))

	iv := &InputView{TextArea: ta, onSend: onSend}

	ta.SetClipboard(
		func(text string) { clipboard.WriteAll(text) }, //nolint:errcheck
		func() string { text, _ := clipboard.ReadAll(); return text },
	)

	ta.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyCtrlS {
			iv.send()
			return nil
		}
		return event
	})

	return iv
}

// SetFocused updates border color to reflect focus.
func (iv *InputView) SetFocused(focused bool) {
	if focused {
		iv.SetBorderColor(Theme.BorderFocus)
		iv.SetTitleColor(Theme.Accent)
	} else {
		iv.SetBorderColor(Theme.Border)
		iv.SetTitleColor(Theme.Header)
	}
}

func (iv *InputView) send() {
	text := strings.TrimSpace(iv.GetText())
	if text == "" || iv.onSend == nil {
		return
	}
	iv.SetText("", true)
	iv.onSend(text)
}

// Clear empties the input field.
func (iv *InputView) Clear() {
	iv.SetText("", true)
}
