package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TabInfo is the display state of one open session.
type TabInfo struct {
	ID      string
	Title   string
	Running bool
}

type tabRange struct {
	id     string
	x1, x2 int
}

type tabClose struct {
	id string
	x  int // screen x of the × character
}

// TabBar is a single-line tab strip shown at the top of the layout.
// It is driven by Sync; the app calls Sync whenever the session list or
// active session changes.
type TabBar struct {
	*tview.Box
	tabs           []TabInfo
	activeID       string
	ranges         []tabRange
	closeButtons   []tabClose
	addBtnX        int // screen x where "+" starts; -1 if not drawn
	onSwitch       func(id string)
	onCloseSession func(id string)
	onNewSession   func()
}

// NewTabBar creates a TabBar.
func NewTabBar(onSwitch func(id string), onCloseSession func(id string), onNewSession func()) *TabBar {
	tb := &TabBar{
		Box:            tview.NewBox(),
		onSwitch:       onSwitch,
		onCloseSession: onCloseSession,
		onNewSession:   onNewSession,
		addBtnX:        -1,
	}
	tb.SetBackgroundColor(Theme.Background)
	return tb
}

// Sync updates the displayed tabs. Must be called on the tview event loop.
func (tb *TabBar) Sync(tabs []TabInfo, activeID string) {
	tb.tabs = tabs
	tb.activeID = activeID
}

// Draw renders the tab bar.
func (tb *TabBar) Draw(screen tcell.Screen) {
	tb.Box.DrawForSubclass(screen, tb)
	x, y, w, _ := tb.GetInnerRect()

	barBg := Theme.Background
	barSt := tcell.StyleDefault.Background(barBg)

	// Clear the row.
	for col := range w {
		screen.SetContent(x+col, y, ' ', nil, barSt)
	}

	tb.ranges = tb.ranges[:0]
	tb.closeButtons = tb.closeButtons[:0]
	tb.addBtnX = -1

	// Reserve space for " + " at the right edge.
	const addBtnW = 3
	availW := w - addBtnW

	cur := 0

	for i, tab := range tb.tabs {
		if cur >= availW {
			break
		}

		isActive := tab.ID == tb.activeID

		// Vertical separator between tabs.
		if i > 0 {
			sepSt := tcell.StyleDefault.Foreground(Theme.Border).Background(barBg)
			screen.SetContent(x+cur, y, '│', nil, sepSt)
			cur++
			if cur >= availW {
				break
			}
		}

		startX := x + cur

		title := tab.Title
		if title == "" {
			title = "new session"
		}
		r := []rune(title)
		if len(r) > 20 {
			r = r[:19]
			title = string(r) + "…"
		}
		title = tview.Escape(title)

		var fgTag, bgTag string
		var tabBg tcell.Color
		if isActive {
			fgTag = TC.Text
			bgTag = TC.Surface
			tabBg = Theme.Surface
		} else {
			fgTag = "#b4b9d4"
			bgTag = TC.Background
			tabBg = Theme.Background
		}

		// Body: " [●] title " — trailing space before ×.
		var body strings.Builder
		fmt.Fprintf(&body, "[%s:%s:-] ", fgTag, bgTag)
		if tab.Running && !isActive {
			fmt.Fprintf(&body, "[%s:%s:-]●[-:%s:-] ", TC.PendingColor, bgTag, bgTag)
		}
		fmt.Fprintf(&body, "[%s:%s:-]%s ", fgTag, bgTag, title)

		// Reserve 2 cols for "× " after body.
		remaining := availW - cur
		if remaining < 3 { // need at least body+× to be meaningful
			break
		}
		_, used := tview.Print(screen, body.String(), x+cur, y, remaining-2, tview.AlignLeft, Theme.Text)
		cur += used

		// Draw × and trailing space.
		closeX := x + cur
		closeSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(tabBg)
		trailSt := tcell.StyleDefault.Background(tabBg)
		screen.SetContent(closeX, y, '×', nil, closeSt)
		screen.SetContent(closeX+1, y, ' ', nil, trailSt)
		cur += 2

		tb.ranges = append(tb.ranges, tabRange{id: tab.ID, x1: startX, x2: x + cur})
		tb.closeButtons = append(tb.closeButtons, tabClose{id: tab.ID, x: closeX})
	}

	// Draw "+" button flush to the right (active-tab background).
	tb.addBtnX = x + w - addBtnW
	addSt := tcell.StyleDefault.Foreground(Theme.Accent).Background(Theme.Surface)
	screen.SetContent(x+w-3, y, ' ', nil, addSt)
	screen.SetContent(x+w-2, y, '+', nil, addSt)
	screen.SetContent(x+w-1, y, ' ', nil, addSt)
}

// MouseHandler handles left-click tab switching, × close, and "+" new session.
func (tb *TabBar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return tb.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := event.Position()
		if !tb.InRect(mx, my) {
			return false, nil
		}
		if action != tview.MouseLeftClick {
			return false, nil
		}

		// "+" button.
		if tb.addBtnX >= 0 && mx >= tb.addBtnX {
			if tb.onNewSession != nil {
				tb.onNewSession()
			}
			return true, nil
		}

		// × close buttons — check before tab range so the close click wins.
		for _, cb := range tb.closeButtons {
			if mx == cb.x {
				if tb.onCloseSession != nil {
					tb.onCloseSession(cb.id)
				}
				return true, nil
			}
		}

		// Tab body click → switch.
		for _, r := range tb.ranges {
			if mx >= r.x1 && mx < r.x2 {
				if r.id != tb.activeID && tb.onSwitch != nil {
					tb.onSwitch(r.id)
				}
				return true, nil
			}
		}
		return false, nil
	})
}
