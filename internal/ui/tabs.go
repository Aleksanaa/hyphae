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
	gapXs          []int  // insertion-marker x positions; len = len(tabs)+1
	addBtnX        int    // screen x where "+" starts; -1 if not drawn
	dragID         string // ID of tab being dragged; empty = no drag
	dragging       bool   // true once moved past threshold
	dragStartX     int
	dragInsertIdx  int // target insertion index during drag (0..len(tabs))
	onSwitch       func(id string)
	onCloseSession func(id string)
	onNewSession   func()
	onReorder      func(id string, insertAt int)
}

// NewTabBar creates a TabBar.
func NewTabBar(onSwitch func(id string), onCloseSession func(id string), onNewSession func(), onReorder func(id string, insertAt int)) *TabBar {
	tb := &TabBar{
		Box:            tview.NewBox(),
		onSwitch:       onSwitch,
		onCloseSession: onCloseSession,
		onNewSession:   onNewSession,
		onReorder:      onReorder,
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

	// Compute gap x-positions for the drag insertion marker.
	// gapXs[0] = before first tab; gapXs[i] = after tab i-1 (at its separator).
	tb.gapXs = tb.gapXs[:0]
	if len(tb.ranges) > 0 {
		tb.gapXs = append(tb.gapXs, tb.ranges[0].x1)
		for _, r := range tb.ranges {
			tb.gapXs = append(tb.gapXs, r.x2)
		}
	}
	if tb.dragging && tb.dragInsertIdx >= 0 && tb.dragInsertIdx < len(tb.gapXs) {
		gx := tb.gapXs[tb.dragInsertIdx]
		screen.SetContent(gx, y, '┃', nil, tcell.StyleDefault.Foreground(Theme.Accent).Background(Theme.Surface))
	}

	// Draw "+" button flush to the right (active-tab background).
	tb.addBtnX = x + w - addBtnW
	addSt := tcell.StyleDefault.Foreground(Theme.Accent).Background(Theme.Surface)
	screen.SetContent(x+w-3, y, ' ', nil, addSt)
	screen.SetContent(x+w-2, y, '+', nil, addSt)
	screen.SetContent(x+w-1, y, ' ', nil, addSt)
}

// computeInsertIdx returns the insertion index (0..len(ranges)) for an x position.
func (tb *TabBar) computeInsertIdx(mx int) int {
	idx := 0
	for i, r := range tb.ranges {
		if mx > (r.x1+r.x2)/2 {
			idx = i + 1
		}
	}
	return idx
}

// MouseHandler handles tab switching, × close, "+" new session, and drag reorder.
func (tb *TabBar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return tb.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := event.Position()

		// Drag tracking is handled for all positions so a drag that exits the bar
		// still completes correctly when the button is released.
		switch action {
		case tview.MouseMove:
			if tb.dragID == "" {
				return false, nil
			}
			if !tb.dragging {
				dx := mx - tb.dragStartX
				if dx < 0 {
					dx = -dx
				}
				if dx >= 3 {
					tb.dragging = true
				}
			}
			if tb.dragging {
				if tb.InRect(mx, my) {
					tb.dragInsertIdx = tb.computeInsertIdx(mx)
				}
				return true, nil
			}
			return false, nil

		case tview.MouseLeftUp:
			if tb.dragging {
				id, insertAt := tb.dragID, tb.dragInsertIdx
				tb.dragID, tb.dragging = "", false
				if tb.onReorder != nil {
					tb.onReorder(id, insertAt)
				}
				return true, nil
			}
			tb.dragID = ""
			return false, nil
		}

		if !tb.InRect(mx, my) {
			return false, nil
		}

		switch action {
		case tview.MouseLeftDown:
			// Record drag start; don't consume so Click still fires for non-drags.
			tb.dragID, tb.dragging = "", false
			if tb.addBtnX >= 0 && mx >= tb.addBtnX {
				break
			}
			onClose := false
			for _, cb := range tb.closeButtons {
				if mx == cb.x {
					onClose = true
					break
				}
			}
			if !onClose {
				for _, r := range tb.ranges {
					if mx >= r.x1 && mx < r.x2 {
						tb.dragID = r.id
						tb.dragStartX = mx
						break
					}
				}
			}

		case tview.MouseLeftClick:
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
		}
		return false, nil
	})
}
