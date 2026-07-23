package ui

import (
	"github.com/rivo/tview"
)

// Input box heights (including the 1-line top+bottom border), stepped down on
// shorter terminals so the conversation keeps usable vertical space: normal
// leaves 4 inner lines (height ≥ 50), medium 3 (35–49), compact 2 (< 35).
const (
	InputHeightNormal  = 6
	InputHeightMedium  = 5
	InputHeightCompact = 4
	mediumInputBelowH  = 50
	compactInputBelowH = 35
)

// TabContent holds all per-session UI components for one tab.
type TabContent struct {
	Chat       *ChatView
	Scrollbar  *Scrollbar
	Input      *InputView
	Status     *StatusBar
	Approval   *ApprovalView
	DiffView   *DiffView
	SelectView *SelectView
	PlanMode   *PlanModeView
	body       *tview.Flex // retained for ResizeItem calls
	Root       *tview.Flex // the full content flex for this tab
}

// Restyle re-applies theme colors to every child view after a theme switch.
func (tc *TabContent) Restyle() {
	tc.Chat.Restyle()
	tc.Input.Restyle()
	tc.Status.Restyle()
	tc.Approval.Restyle()
	tc.DiffView.Restyle()
	tc.SelectView.Restyle()
	tc.PlanMode.Restyle()
}

// SetInputHeightForScreen steps the input box height down on shorter terminals
// so the conversation keeps usable vertical space.
func (tc *TabContent) SetInputHeightForScreen(screenH int) {
	h := InputHeightNormal
	switch {
	case screenH < compactInputBelowH:
		h = InputHeightCompact
	case screenH < mediumInputBelowH:
		h = InputHeightMedium
	}
	tc.body.ResizeItem(tc.Input, h, 0)
}

func (tc *TabContent) ShowApproval(height int) {
	tc.body.ResizeItem(tc.Approval, height, 0)
}

func (tc *TabContent) HideApproval() {
	tc.Approval.visible = false
	tc.body.ResizeItem(tc.Approval, 0, 0)
}

func (tc *TabContent) ShowDiffView() {
	tc.body.ResizeItem(tc.DiffView, DiffViewHeight, 0)
}

func (tc *TabContent) HideDiffView() {
	tc.DiffView.visible = false
	tc.body.ResizeItem(tc.DiffView, 0, 0)
}

func (tc *TabContent) ShowSelect(height int) {
	tc.body.ResizeItem(tc.SelectView, height, 0)
}

func (tc *TabContent) HideSelect() {
	tc.SelectView.visible = false
	tc.body.ResizeItem(tc.SelectView, 0, 0)
}

func (tc *TabContent) ShowPlanMode() {
	tc.PlanMode.Show()
	tc.body.ResizeItem(tc.PlanMode, PlanModeHeight, 0)
}

func (tc *TabContent) HidePlanMode() {
	tc.PlanMode.visible = false
	tc.body.ResizeItem(tc.PlanMode, 0, 0)
}
