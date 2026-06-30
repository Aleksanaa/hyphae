package ui

import (
	"fmt"

	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/session"
)

// SidebarView shows the list of sessions and a "New" action.
type SidebarView struct {
	*tview.List
	onSelect func(sessionID string)
	onNew    func()
	sessions []*session.Session
}

// NewSidebarView creates the session list panel.
func NewSidebarView(onSelect func(string), onNew func()) *SidebarView {
	list := tview.NewList()
	list.SetBorder(true)
	list.SetTitle(" sessions ")
	list.SetTitleColor(Theme.Header)
	list.SetBorderColor(Theme.Border)
	list.SetBackgroundColor(Theme.Surface)
	list.SetMainTextColor(Theme.Text)
	list.SetSelectedTextColor(Theme.Background)
	list.SetSelectedBackgroundColor(Theme.Accent)
	list.SetSecondaryTextColor(Theme.Muted)
	list.ShowSecondaryText(false)

	sv := &SidebarView{
		List:     list,
		onSelect: onSelect,
		onNew:    onNew,
	}
	sv.rebuild()
	return sv
}

// SetFocused updates border color to reflect focus.
func (sv *SidebarView) SetFocused(focused bool) {
	if focused {
		sv.SetBorderColor(Theme.BorderFocus)
	} else {
		sv.SetBorderColor(Theme.Border)
	}
}

// Update refreshes the list with a new set of sessions.
func (sv *SidebarView) Update(sessions []*session.Session, activeID string) {
	sv.sessions = sessions
	sv.rebuild()

	// restore selection to the active session
	for i, s := range sv.sessions {
		if s.ID == activeID {
			sv.SetCurrentItem(i)
			return
		}
	}
}

func (sv *SidebarView) rebuild() {
	sv.Clear()

	for _, s := range sv.sessions {
		id := s.ID
		title := s.Title
		if title == "" {
			title = id
		}

		_, status := s.Snapshot()
		indicator := " "
		switch status {
		case session.StatusRunning:
			indicator = "●"
		case session.StatusError:
			indicator = "✗"
		}

		label := fmt.Sprintf("%s %s", indicator, tview.Escape(title))
		sv.AddItem(label, id, 0, func() {
			if sv.onSelect != nil {
				sv.onSelect(id)
			}
		})
	}

	// "New session" entry at the bottom
	sv.AddItem(
		fmt.Sprintf("[%s]+ new session[-]", tviewColor(Theme.Accent)),
		"", 0,
		func() {
			if sv.onNew != nil {
				sv.onNew()
			}
		},
	)
}
