package ui

import (
	"github.com/rivo/tview"
)

// Layout assembles the top-level flex container.
type Layout struct {
	Root      *tview.Pages
	Tabs      *TabBar
	BodyPages *tview.Pages
	Palette   *CommandPalette
}

// NewLayout builds and returns the assembled layout.
func NewLayout(tabs *TabBar, palette *CommandPalette) *Layout {
	bodyPages := tview.NewPages()

	mainFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tabs, 1, 0, false).
		AddItem(bodyPages, 0, 1, true)

	pages := tview.NewPages()
	pages.AddPage("main", mainFlex, true, true)
	pages.AddPage("palette", palette, true, false)

	return &Layout{
		Root:      pages,
		Tabs:      tabs,
		BodyPages: bodyPages,
		Palette:   palette,
	}
}

// AddTabContent registers a tab's content as a named page.
func (l *Layout) AddTabContent(id string, tc *TabContent) {
	l.BodyPages.AddPage(id, tc.Root, true, false)
}

// ShowTab switches the body to display the given session's content.
func (l *Layout) ShowTab(id string) {
	l.BodyPages.SwitchToPage(id)
}

// RemoveTab removes a session's content page.
func (l *Layout) RemoveTab(id string) {
	l.BodyPages.RemovePage(id)
}

// ShowPalette makes the palette overlay visible.
func (l *Layout) ShowPalette() {
	l.Root.ShowPage("palette")
}

// HidePalette hides the palette overlay.
func (l *Layout) HidePalette() {
	l.Root.HidePage("palette")
}
