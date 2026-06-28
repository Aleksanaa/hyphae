package ui

import (
	"github.com/rivo/tview"
)

// Layout assembles the top-level flex container.
type Layout struct {
	Root      *tview.Pages
	Chat      *ChatView
	Scrollbar *Scrollbar
	Input     *InputView
	Status    *StatusBar
	Approval  *ApprovalView
	DiffView  *DiffView
	Palette   *CommandPalette
	body      *tview.Flex // retained for ResizeItem calls
}

// NewLayout builds and returns the assembled layout.
func NewLayout(chat *ChatView, scrollbar *Scrollbar, input *InputView, status *StatusBar, approval *ApprovalView, diffView *DiffView, palette *CommandPalette) *Layout {
	chatRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(chat, 0, 1, false).
		AddItem(scrollbar, 1, 0, false)

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(chatRow, 0, 1, false).
		AddItem(approval, 0, 0, false). // hidden initially
		AddItem(diffView, 0, 0, false). // hidden initially
		AddItem(input, 6, 0, true)

	mainFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, true).
		AddItem(status, 1, 0, false)

	pages := tview.NewPages()
	pages.AddPage("main", mainFlex, true, true)
	pages.AddPage("palette", palette, true, false) // overlay, hidden initially

	return &Layout{
		Root:      pages,
		Chat:      chat,
		Scrollbar: scrollbar,
		Input:     input,
		Status:    status,
		Approval:  approval,
		DiffView:  diffView,
		Palette:   palette,
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

// ShowDiffView makes the diff approval view visible.
func (l *Layout) ShowDiffView() {
	l.body.ResizeItem(l.DiffView, DiffViewHeight, 0)
}

// HideDiffView collapses the diff approval view.
func (l *Layout) HideDiffView() {
	l.DiffView.visible = false
	l.body.ResizeItem(l.DiffView, 0, 0)
}

// ShowPalette makes the palette page visible.
func (l *Layout) ShowPalette() {
	l.Root.ShowPage("palette")
}

// HidePalette hides the palette page.
func (l *Layout) HidePalette() {
	l.Root.HidePage("palette")
}
