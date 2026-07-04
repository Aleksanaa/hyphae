package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/session"
)

const barWidth = 10

// barEmptyBg is the background color for unfilled bar cells.
var barEmptyBg = tcell.NewRGBColor(40, 44, 60)

// bar fill colors — darker than the theme equivalents to avoid visual glare on solid blocks.
var (
	barFillBlue  = tcell.NewRGBColor(40, 70, 160)
	barFillAmber = tcell.NewRGBColor(120, 90, 20)
	barFillRed   = tcell.NewRGBColor(130, 38, 38)
)

// subBlockRunes maps eighths (1–7) to the corresponding Unicode block element.
var subBlockRunes = []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

func formatCost(usd float64) string {
	switch {
	case usd < 0.01:
		return fmt.Sprintf("$%.4f", usd)
	default:
		return fmt.Sprintf("$%.2f", usd)
	}
}

// StatusBar renders one-line context at the bottom of the screen.
// Left side: status indicator + model name.
// Right side: token usage progress bar (when context window is known).
type StatusBar struct {
	*tview.Box
	model         string
	status        session.Status
	selActive     bool
	promptTokens  int64
	contextWindow int64

	sessionCost float64

	// rendered cache
	left     string      // tview-tagged left content
	costText string      // tview-tagged cost label, left of bar
	pctText  string      // tview-tagged right text (percentage or tok count)
	barPct   float64     // 0..1, used in drawBar; 0 when no context window
	barFill  tcell.Color // fill color for the bar; default when no bar
}

// NewStatusBar creates a styled status bar primitive.
func NewStatusBar() *StatusBar {
	sb := &StatusBar{Box: tview.NewBox()}
	sb.SetBackgroundColor(Theme.StatusBg)
	sb.SetBorder(false)
	sb.render()
	return sb
}

func (sb *StatusBar) SetDefault(model string, status session.Status) {
	sb.model = model
	sb.status = status
	sb.render()
}

func (sb *StatusBar) SetSelActive(active bool) {
	if sb.selActive == active {
		return
	}
	sb.selActive = active
	sb.render()
}

func (sb *StatusBar) SetPromptTokens(n int64) {
	sb.promptTokens = n
	sb.render()
}

func (sb *StatusBar) SetContextWindow(cw int64) {
	sb.contextWindow = cw
	sb.render()
}

func (sb *StatusBar) SetSessionCost(usd float64) {
	sb.sessionCost = usd
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
	sb.left = fmt.Sprintf(" %s[%s]%s[-]", statusStr, TC.Muted, tview.Escape(modelStr))

	if sb.sessionCost > 0 {
		sb.costText = fmt.Sprintf("[%s]%s[-] ", TC.Muted, formatCost(sb.sessionCost))
	} else {
		sb.costText = ""
	}

	switch {
	case sb.promptTokens > 0 && sb.contextWindow > 0:
		pct := float64(sb.promptTokens) / float64(sb.contextWindow)
		if pct > 1 {
			pct = 1
		}
		sb.barPct = pct

		fillColor := barFillBlue
		cssColor := TC.Accent
		switch {
		case pct >= 0.9:
			fillColor = barFillRed
			cssColor = TC.ErrorColor
		case pct >= 0.75:
			fillColor = barFillAmber
			cssColor = TC.PendingColor
		}
		sb.barFill = fillColor
		sb.pctText = fmt.Sprintf("[%s] %.1f%%[-] ", cssColor, pct*100)

	case sb.promptTokens > 0:
		sb.barPct = 0
		sb.barFill = tcell.ColorDefault
		sb.pctText = fmt.Sprintf("[%s]%d tok[-] ", TC.Muted, sb.promptTokens)

	default:
		sb.barPct = 0
		sb.barFill = tcell.ColorDefault
		sb.pctText = ""
	}
}

// drawBar renders the progress bar directly to the screen at (x, y) with given width.
// Filled cells use block chars; empty cells use background color — no gap-inducing runes.
func (sb *StatusBar) drawBar(screen tcell.Screen, x, y, width int) {
	pct := sb.barPct
	n := int(pct * float64(width*8))
	fullCells := n / 8
	partial := n % 8

	for col := range width {
		var ch rune
		var st tcell.Style
		switch {
		case col < fullCells:
			ch = '█'
			st = tcell.StyleDefault.Foreground(sb.barFill).Background(sb.barFill)
		case col == fullCells && partial > 0:
			ch = subBlockRunes[partial-1]
			st = tcell.StyleDefault.Foreground(sb.barFill).Background(barEmptyBg)
		default:
			ch = ' '
			st = tcell.StyleDefault.Background(barEmptyBg)
		}
		screen.SetContent(x+col, y, ch, nil, st)
	}
}

func (sb *StatusBar) Draw(screen tcell.Screen) {
	sb.Box.DrawForSubclass(screen, sb)
	x, y, w, _ := sb.GetInnerRect()
	if w <= 0 {
		return
	}

	bg := Theme.StatusBg
	for col := range w {
		screen.SetContent(x+col, y, ' ', nil, tcell.StyleDefault.Background(bg))
	}

	leftW, _ := tview.Print(screen, sb.left, x, y, w, tview.AlignLeft, Theme.Text)

	paletteHint := fmt.Sprintf("[%s]Ctrl-P[-] [%s]palette[-]  ", TC.Accent, TC.Muted)
	paletteW := tview.TaggedStringWidth(paletteHint)
	pctW := tview.TaggedStringWidth(sb.pctText)
	costW := tview.TaggedStringWidth(sb.costText)
	hasBar := sb.barPct > 0 || (sb.promptTokens > 0 && sb.contextWindow > 0)
	rightW := paletteW + costW + pctW
	if hasBar {
		rightW += barWidth
	}
	rightX := x + w - rightW
	if rightX > x+leftW {
		tview.Print(screen, paletteHint, rightX, y, paletteW, tview.AlignLeft, Theme.Text)
		if costW > 0 {
			tview.Print(screen, sb.costText, rightX+paletteW, y, costW, tview.AlignLeft, Theme.Text)
		}
		barX := rightX + paletteW + costW
		if hasBar {
			sb.drawBar(screen, barX, y, barWidth)
			tview.Print(screen, sb.pctText, barX+barWidth, y, pctW, tview.AlignLeft, Theme.Text)
		} else if pctW > 0 {
			tview.Print(screen, sb.pctText, barX, y, pctW, tview.AlignLeft, Theme.Text)
		}
	}
}

func (sb *StatusBar) SetMessage(msg string) {
	sb.left = fmt.Sprintf(" [%s]%s[-]", TC.Accent, tview.Escape(msg))
}

func (sb *StatusBar) SetError(err string) {
	sb.left = fmt.Sprintf(" [%s]✗ %s[-]", TC.ErrorColor, tview.Escape(err))
}
