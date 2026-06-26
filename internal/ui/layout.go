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
}

// NewLayout builds and returns the assembled layout.
func NewLayout(chat *ChatView, scrollbar *Scrollbar, input *InputView, status *StatusBar) *Layout {
	// Chat + scrollbar side by side
	chatRow := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(chat, 0, 1, false).
		AddItem(scrollbar, 1, 0, false)

	// Main panel: chat row + input
	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(chatRow, 0, 1, false).
		AddItem(input, 6, 0, true)

	// Root: body + status bar
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, true).
		AddItem(status, 1, 0, false)

	return &Layout{
		Root:      root,
		Chat:      chat,
		Scrollbar: scrollbar,
		Input:     input,
		Status:    status,
	}
}
