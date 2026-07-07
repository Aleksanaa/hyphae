package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// scrollIndW is the screen width of the "…" scroll indicator on either side.
const scrollIndW = 3

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
	gapXs          []int  // insertion-marker x positions; len = len(visible tabs)+1
	addBtnX        int    // screen x where "+" starts; -1 if not drawn
	offset         int    // index of first visible tab
	leftScrollX    int    // screen x of left "…" indicator; -1 if not shown
	rightScrollX   int    // screen x of right "…" indicator; -1 if not shown
	dragID         string // ID of tab being dragged; empty = no drag
	dragging       bool   // true once moved past threshold
	dragStartX     int
	dragInsertIdx  int // target insertion index during drag (0..len(visible tabs))
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
		leftScrollX:    -1,
		rightScrollX:   -1,
	}
	tb.SetBackgroundColor(Theme.Background)
	return tb
}

// Sync updates the displayed tabs. Must be called on the tview event loop.
func (tb *TabBar) Sync(tabs []TabInfo, activeID string) {
	tb.tabs = tabs
	tb.activeID = activeID
}

// computeBodyWidth returns the screen width of a tab's rendered body including
// the leading space, optional running indicator, title, trailing space, × and
// its trailing space — but NOT the separator before it.
func (tb *TabBar) computeBodyWidth(tab TabInfo) int {
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

	isActive := tab.ID == tb.activeID
	var fgTag, bgTag string
	if isActive {
		fgTag = TC.Text
		bgTag = TC.Surface
	} else {
		fgTag = "#b4b9d4"
		bgTag = TC.Background
	}

	var body strings.Builder
	fmt.Fprintf(&body, "[%s:%s:-] ", fgTag, bgTag)
	if tab.Running && !isActive {
		fmt.Fprintf(&body, "[%s:%s:-]●[-:%s:-] ", TC.PendingColor, bgTag, bgTag)
	}
	fmt.Fprintf(&body, "[%s:%s:-]%s ", fgTag, bgTag, title)

	return tview.TaggedStringWidth(body.String()) + 2 // +2 for × and trailing space
}

// computeVisEnd returns the exclusive end index of the visible tab range starting
// at offset, and whether a right scroll indicator is needed.
func (tb *TabBar) computeVisEnd(bodyWidths []int, availW, offset int) (visEnd int, needRight bool) {
	n := len(bodyWidths)

	leftW := 0
	if offset > 0 {
		leftW = scrollIndW
	}

	// First try without a right indicator.
	inner := availW - leftW
	used := 0
	visEnd = offset
	for visEnd < n {
		tw := bodyWidths[visEnd]
		if visEnd > offset {
			tw++ // separator
		}
		if used+tw > inner {
			break
		}
		used += tw
		visEnd++
	}
	if visEnd >= n {
		return visEnd, false
	}

	// Right indicator needed: redo with fewer cells.
	inner = availW - leftW - scrollIndW
	used = 0
	visEnd = offset
	for visEnd < n {
		tw := bodyWidths[visEnd]
		if visEnd > offset {
			tw++
		}
		if used+tw > inner {
			break
		}
		used += tw
		visEnd++
	}
	return visEnd, true
}

// Draw renders the tab bar.
func (tb *TabBar) Draw(screen tcell.Screen) {
	tb.Box.DrawForSubclass(screen, tb)
	x, y, w, _ := tb.GetInnerRect()

	barBg := Theme.Background
	barSt := tcell.StyleDefault.Background(barBg)
	for col := range w {
		screen.SetContent(x+col, y, ' ', nil, barSt)
	}

	tb.ranges = tb.ranges[:0]
	tb.closeButtons = tb.closeButtons[:0]
	tb.gapXs = tb.gapXs[:0]
	tb.addBtnX = -1
	tb.leftScrollX = -1
	tb.rightScrollX = -1

	const addBtnW = 3
	availW := w - addBtnW
	n := len(tb.tabs)

	if n == 0 {
		goto drawAddBtn
	}

	{
		// Precompute body widths.
		bodyWidths := make([]int, n)
		for i, tab := range tb.tabs {
			bodyWidths[i] = tb.computeBodyWidth(tab)
		}

		// Find active tab index.
		activeIdx := -1
		for i, t := range tb.tabs {
			if t.ID == tb.activeID {
				activeIdx = i
				break
			}
		}

		// Clamp offset.
		if tb.offset < 0 {
			tb.offset = 0
		}
		if tb.offset >= n {
			tb.offset = n - 1
		}

		// Auto-scroll left: active tab is before the view.
		if activeIdx >= 0 && activeIdx < tb.offset {
			tb.offset = activeIdx
		}

		// Auto-scroll right: keep incrementing offset until active tab is in view.
		visEnd, needRight := tb.computeVisEnd(bodyWidths, availW, tb.offset)
		for activeIdx >= 0 && visEnd <= activeIdx && tb.offset < activeIdx {
			tb.offset++
			visEnd, needRight = tb.computeVisEnd(bodyWidths, availW, tb.offset)
		}

		needLeft := tb.offset > 0

		// --- Render ---
		cur := 0
		indSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(barBg)

		if needLeft {
			tb.leftScrollX = x + cur
			screen.SetContent(x+cur, y, ' ', nil, indSt)
			screen.SetContent(x+cur+1, y, '…', nil, indSt)
			screen.SetContent(x+cur+2, y, ' ', nil, indSt)
			cur += scrollIndW
		}

		for i := tb.offset; i < visEnd; i++ {
			tab := tb.tabs[i]
			isActive := tab.ID == tb.activeID

			if i > tb.offset {
				sepSt := tcell.StyleDefault.Foreground(Theme.Border).Background(barBg)
				screen.SetContent(x+cur, y, '│', nil, sepSt)
				cur++
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

			var body strings.Builder
			fmt.Fprintf(&body, "[%s:%s:-] ", fgTag, bgTag)
			if tab.Running && !isActive {
				fmt.Fprintf(&body, "[%s:%s:-]●[-:%s:-] ", TC.PendingColor, bgTag, bgTag)
			}
			fmt.Fprintf(&body, "[%s:%s:-]%s ", fgTag, bgTag, title)

			remaining := availW - cur
			_, used := tview.Print(screen, body.String(), x+cur, y, remaining-2, tview.AlignLeft, Theme.Text)
			cur += used

			closeX := x + cur
			closeSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(tabBg)
			trailSt := tcell.StyleDefault.Background(tabBg)
			screen.SetContent(closeX, y, '×', nil, closeSt)
			screen.SetContent(closeX+1, y, ' ', nil, trailSt)
			cur += 2

			tb.ranges = append(tb.ranges, tabRange{id: tab.ID, x1: startX, x2: x + cur})
			tb.closeButtons = append(tb.closeButtons, tabClose{id: tab.ID, x: closeX})
		}

		if needRight {
			tb.rightScrollX = x + cur
			screen.SetContent(x+cur, y, ' ', nil, indSt)
			screen.SetContent(x+cur+1, y, '…', nil, indSt)
			screen.SetContent(x+cur+2, y, ' ', nil, indSt)
		}

		// Gap positions for drag insertion marker.
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
	}

drawAddBtn:
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

// MouseHandler handles tab switching, × close, "+" new session, scroll, and drag reorder.
func (tb *TabBar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return tb.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := event.Position()

		// Drag tracking: handle even outside the bar so a drag that exits still
		// completes correctly on button release.
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
				// dragInsertIdx is relative to visible tabs; convert to absolute.
				id, insertAt := tb.dragID, tb.offset+tb.dragInsertIdx
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
		case tview.MouseScrollUp:
			if tb.offset > 0 {
				tb.offset--
			}
			return true, nil

		case tview.MouseScrollDown:
			if tb.offset < len(tb.tabs)-1 {
				tb.offset++
			}
			return true, nil

		case tview.MouseLeftDown:
			// Record drag start; don't consume so Click still fires for non-drags.
			tb.dragID, tb.dragging = "", false
			// Scroll indicators and + button are not draggable.
			if tb.addBtnX >= 0 && mx >= tb.addBtnX {
				break
			}
			if (tb.leftScrollX >= 0 && mx >= tb.leftScrollX && mx < tb.leftScrollX+scrollIndW) ||
				(tb.rightScrollX >= 0 && mx >= tb.rightScrollX && mx < tb.rightScrollX+scrollIndW) {
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
			// Left scroll indicator.
			if tb.leftScrollX >= 0 && mx >= tb.leftScrollX && mx < tb.leftScrollX+scrollIndW {
				if tb.offset > 0 {
					tb.offset--
				}
				return true, nil
			}
			// Right scroll indicator.
			if tb.rightScrollX >= 0 && mx >= tb.rightScrollX && mx < tb.rightScrollX+scrollIndW {
				tb.offset++
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
