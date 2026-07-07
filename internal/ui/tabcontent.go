package ui

import (
	"github.com/rivo/tview"
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
	body       *tview.Flex // retained for ResizeItem calls
	Root       *tview.Flex // the full content flex for this tab
}

func (tc *TabContent) ShowApproval() {
	tc.body.ResizeItem(tc.Approval, ApprovalHeight, 0)
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
