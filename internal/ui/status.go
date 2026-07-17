package ui

import (
	"fmt"
	"strings"

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

	// onStatusClick / onModelClick, when set, fire on a left-click of the status
	// indicator or the model name respectively (VSCode-style: click the state to
	// open the command palette, click the model to open model selection).
	onStatusClick func()
	onModelClick  func()

	// rendered cache
	left       string      // tview-tagged left content
	statusHitW int         // width of the clickable status-indicator region, from the left edge
	modelHitX  int         // left offset of the clickable model-name region
	modelHitW  int         // width of the clickable model-name region
	costText   string      // tview-tagged cost label, left of bar
	pctText    string      // tview-tagged right text (percentage or tok count)
	barPct     float64     // 0..1, used in drawBar; 0 when no context window
	barFill    tcell.Color // fill color for the bar; default when no bar
}

// SetStatusClickFunc registers a callback invoked when the status indicator
// (bottom-left "idle/running/…" text) is left-clicked.
func (sb *StatusBar) SetStatusClickFunc(fn func()) {
	sb.onStatusClick = fn
}

// SetModelClickFunc registers a callback invoked when the model name is left-clicked.
func (sb *StatusBar) SetModelClickFunc(fn func()) {
	sb.onModelClick = fn
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
	case session.StatusConnecting:
		statusStr = fmt.Sprintf("[%s]○ connecting[-]  ", TC.Accent)
	case session.StatusRunning:
		statusStr = fmt.Sprintf("[%s]● running[-]  ", TC.SuccessColor)
	case session.StatusWaiting:
		statusStr = fmt.Sprintf("[%s]● waiting[-]  ", TC.PendingColor)
	case session.StatusCompacting:
		statusStr = fmt.Sprintf("[%s]● compacting[-]  ", TC.Accent)
	case session.StatusError:
		statusStr = fmt.Sprintf("[%s]✗ error[-]  ", TC.ErrorColor)
	default:
		statusStr = fmt.Sprintf("[%s]○ idle[-]  ", TC.Muted)
	}
	sb.left = fmt.Sprintf(" %s[%s]%s[-]", statusStr, TC.Muted, tview.Escape(modelStr))
	// Clickable regions: the leading space plus the status indicator text (its
	// trailing padding is excluded so we don't claim the gap before the model),
	// and the model name itself, which begins after the full status prefix.
	sb.statusHitW = 1 + tview.TaggedStringWidth(strings.TrimRight(statusStr, " "))
	sb.modelHitX = 1 + tview.TaggedStringWidth(statusStr)
	sb.modelHitW = tview.TaggedStringWidth(modelStr)

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
	hasBar := sb.promptTokens > 0 && sb.contextWindow > 0
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

// MouseHandler opens the command palette when the status indicator is clicked,
// or model selection when the model name is clicked.
func (sb *StatusBar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return sb.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if action != tview.MouseLeftClick {
			return false, nil
		}
		x, y, _, _ := sb.GetInnerRect()
		mx, my := event.Position()
		if my != y {
			return false, nil
		}
		rel := mx - x
		switch {
		case sb.onStatusClick != nil && rel >= 0 && rel < sb.statusHitW:
			sb.onStatusClick()
			return true, nil
		case sb.onModelClick != nil && rel >= sb.modelHitX && rel < sb.modelHitX+sb.modelHitW:
			sb.onModelClick()
			return true, nil
		}
		return false, nil
	})
}

// Reset restores the standard model/status display, clearing any temporary
// SetMessage or SetError override.
func (sb *StatusBar) Reset() {
	sb.render()
}

func (sb *StatusBar) SetMessage(msg string) {
	sb.left = fmt.Sprintf(" [%s]%s[-]", TC.Accent, tview.Escape(msg))
}

func (sb *StatusBar) SetError(err string) {
	sb.left = fmt.Sprintf(" [%s]✗ %s[-]", TC.ErrorColor, tview.Escape(err))
}
