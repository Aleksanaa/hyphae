package ui

import (
	"fmt"

	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/session"
)

// StatusBar renders one-line context at the bottom of the screen.
type StatusBar struct {
	*tview.TextView
	model     string
	status    session.Status
	selActive bool
}

// NewStatusBar creates a styled status bar primitive.
func NewStatusBar() *StatusBar {
	tv := tview.NewTextView()
	tv.SetDynamicColors(true)
	tv.SetBackgroundColor(Theme.StatusBg)
	tv.SetBorder(false)

	sb := &StatusBar{TextView: tv}
	sb.SetDefault("", session.StatusIdle)
	return sb
}

// SetDefault stores model+status and re-renders.
func (sb *StatusBar) SetDefault(model string, status session.Status) {
	sb.model = model
	sb.status = status
	sb.render()
}

// SetSelActive updates whether a selection is active and re-renders.
func (sb *StatusBar) SetSelActive(active bool) {
	if sb.selActive == active {
		return
	}
	sb.selActive = active
	sb.render()
}

func (sb *StatusBar) render() {
	modelStr := sb.model
	if modelStr == "" {
		modelStr = "no model"
	}

	statusStr := ""
	switch sb.status {
	case session.StatusRunning:
		statusStr = fmt.Sprintf("[%s]● running[-]  ", TC.SuccessColor)
	case session.StatusError:
		statusStr = fmt.Sprintf("[%s]✗ error[-]  ", TC.ErrorColor)
	default:
		statusStr = fmt.Sprintf("[%s]○ idle[-]  ", TC.Muted)
	}

	ctrlC := "interrupt"
	if sb.selActive {
		ctrlC = "copy"
	}

	ac := TC.Accent
	hints := fmt.Sprintf(
		"[%s]Ctrl+S[-]:send  [%s]Ctrl+C[-]:%s  [%s]Ctrl+P[-]:palette  [%s]Tab[-]:focus  [%s]Ctrl+D[-]:quit",
		ac, ac, ctrlC, ac, ac, ac,
	)

	sb.SetText(fmt.Sprintf(" %s[%s]%s[-]  %s",
		statusStr,
		TC.Muted,
		tview.Escape(modelStr),
		hints,
	))
}

// SetMessage temporarily displays an informational message.
func (sb *StatusBar) SetMessage(msg string) {
	sb.SetText(fmt.Sprintf(" [%s]%s[-]", TC.Accent, tview.Escape(msg)))
}

// SetError shows an error in red.
func (sb *StatusBar) SetError(err string) {
	sb.SetText(fmt.Sprintf(" [%s]✗ %s[-]", TC.ErrorColor, tview.Escape(err)))
}
