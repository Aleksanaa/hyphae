package ui

import (
	"github.com/rivo/tview"
)

// Layout assembles the top-level flex container.
type Layout struct {
	Root      *tview.Flex
	Chat      *ChatView
	Scrollbar *Scrollbar
	Input     *InputView
	Status    *StatusBar
	Approval  *ApprovalView
	body      *tview.Flex // retained for ResizeItem calls
}

// NewLayout builds and returns the assembled layout.
func NewLayout(chat *ChatView, scrollbar *Scrollbar, input *InputView, status *StatusBar, approval *ApprovalView) *Layout {
	chatRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(chat, 0, 1, false).
		AddItem(scrollbar, 1, 0, false)

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(chatRow, 0, 1, false).
		AddItem(approval, 0, 0, false). // hidden initially
		AddItem(input, 6, 0, true)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, true).
		AddItem(status, 1, 0, false)

	return &Layout{
		Root:      root,
		Chat:      chat,
		Scrollbar: scrollbar,
		Input:     input,
		Status:    status,
		Approval:  approval,
		body:      body,
	}
}

// ShowApproval makes the approval bar visible.
func (l *Layout) ShowApproval() {
	l.body.ResizeItem(l.Approval, ApprovalHeight, 0)
}

// HideApproval collapses the approval bar.
func (l *Layout) HideApproval() {
	l.Approval.visible = false
	l.body.ResizeItem(l.Approval, 0, 0)
}
