package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// PlanModeHeight is the fixed row count of the plan mode bar when visible.
const PlanModeHeight = 3

// PlanModeView is a persistent indicator shown when a session is in plan mode.
// It sits between the chat and input. When hidden its height is 0.
type PlanModeView struct {
	*tview.Box
	visible  bool
	exitBtnX int // screen x of "[ Exit ]"; -1 if not drawn
	onExit   func()
}

func NewPlanModeView(onExit func()) *PlanModeView {
	pv := &PlanModeView{
		Box:      tview.NewBox(),
		exitBtnX: -1,
		onExit:   onExit,
	}
	pv.SetBackgroundColor(Theme.Surface)
	pv.SetBorder(true)
	pv.SetBorderColor(Theme.SuccessColor)
	pv.SetTitleColor(Theme.SuccessColor)
	pv.SetTitle(" plan mode ")
	pv.SetTitleAlign(tview.AlignLeft)
	return pv
}

func (pv *PlanModeView) IsVisible() bool { return pv.visible }

func (pv *PlanModeView) Show() { pv.visible = true }

func (pv *PlanModeView) Draw(screen tcell.Screen) {
	pv.Box.DrawForSubclass(screen, pv)
	pv.exitBtnX = -1
	if !pv.visible {
		return
	}
	x, y, w, _ := pv.GetRect()
	if w < 10 {
		return
	}

	bg := Theme.Surface
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(bg)
	drawText(screen, "🔒 Only explore and plan without making changes", x+2, y+1, w-4, textSt)

	const btn = "[ Exit ]"
	btnX := x + w - 2 - len([]rune(btn))
	pv.exitBtnX = btnX
	exitSt := tcell.StyleDefault.Background(approvalDarkRed).Foreground(approvalWhite)
	drawText(screen, btn, btnX, y+1, len([]rune(btn)), exitSt)
}

func (pv *PlanModeView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return pv.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !pv.visible {
			return false, nil
		}
		mx, my := event.Position()
		_, y, _, _ := pv.GetRect()
		if my != y+1 || pv.exitBtnX < 0 {
			return false, nil
		}
		const btn = "[ Exit ]"
		if mx < pv.exitBtnX || mx >= pv.exitBtnX+len([]rune(btn)) {
			return false, nil
		}
		if action == tview.MouseLeftClick && pv.onExit != nil {
			pv.onExit()
			return true, nil
		}
		return action == tview.MouseLeftDown, nil
	})
}
