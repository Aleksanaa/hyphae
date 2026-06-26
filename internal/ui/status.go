package ui

import (
	"fmt"

	"github.com/rivo/tview"

	"github.com/aleksana/hypane/internal/session"
)

// StatusBar renders one-line context at the bottom of the screen.
type StatusBar struct {
	*tview.TextView
}

// NewStatusBar creates a styled status bar primitive.
func NewStatusBar() *StatusBar {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetBackgroundColor(Theme.StatusBg)
	tv.SetBorder(false)

	sb := &StatusBar{tv}
	sb.SetDefault("", session.StatusIdle)
	return sb
}

// SetDefault renders the status for a given model + agent status.
func (sb *StatusBar) SetDefault(model string, status session.Status) {
	modelStr := model
	if modelStr == "" {
		modelStr = "no model"
	}

	statusStr := ""
	switch status {
	case session.StatusRunning:
		statusStr = fmt.Sprintf("[%s]● running[-]  ", tviewColor(Theme.SuccessColor))
	case session.StatusError:
		statusStr = fmt.Sprintf("[%s]✗ error[-]  ", tviewColor(Theme.ErrorColor))
	default:
		statusStr = fmt.Sprintf("[%s]○ idle[-]  ", tviewColor(Theme.Muted))
	}

	hints := fmt.Sprintf(
		"[%s]Ctrl+S[-]:send  [%s]Ctrl+C[-]:interrupt  [%s]Tab[-]:focus  [%s]Ctrl+D[-]:quit",
		tviewColor(Theme.Accent), tviewColor(Theme.Accent),
		tviewColor(Theme.Accent), tviewColor(Theme.Accent),
	)

	sb.SetText(fmt.Sprintf(" %s[%s]%s[-]  %s",
		statusStr,
		tviewColor(Theme.Muted),
		modelStr,
		hints,
	))
}

// SetMessage temporarily displays an informational message.
func (sb *StatusBar) SetMessage(msg string) {
	sb.SetText(fmt.Sprintf(" [%s]%s[-]", tviewColor(Theme.Accent), msg))
}

// SetError shows an error in red.
func (sb *StatusBar) SetError(err string) {
	sb.SetText(fmt.Sprintf(" [%s]✗ %s[-]", tviewColor(Theme.ErrorColor), err))
}

