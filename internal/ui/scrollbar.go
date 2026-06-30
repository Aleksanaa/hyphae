package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Scrollbar is a 1-column primitive that reflects and controls a scroll position.
type Scrollbar struct {
	*tview.Box
	getTotal   func() int
	getPageH   func() int
	getScrollY func() int
	scrollTo   func(int)
	dragging    bool
	lastScrollY int
}

// NewScrollbar creates a scrollbar driven by the provided callbacks.
// getTotal returns total content lines; getPageH returns visible page height;
// getScrollY returns the current scroll offset; scrollTo sets the offset.
func NewScrollbar(getTotal, getPageH, getScrollY func() int, scrollTo func(int)) *Scrollbar {
	return &Scrollbar{
		Box:        tview.NewBox(),
		getTotal:   getTotal,
		getPageH:   getPageH,
		getScrollY: getScrollY,
		scrollTo:   scrollTo,
	}
}

// drawScrollbarTrack renders a scrollbar track and thumb at column x starting
// at row y. trackH is the visible height of the scrollbar. total is the total
// content rows, pageH is the number of rows visible in the content area, and
// scrollTop is the current scroll offset. trackR/thumbR are the runes for the
// track and thumb; trackSt/thumbSt are their styles.
func drawScrollbarTrack(screen tcell.Screen, x, y, trackH, total, pageH, scrollTop int, trackR, thumbR rune, trackSt, thumbSt tcell.Style) {
	for i := range trackH {
		screen.SetContent(x, y+i, trackR, nil, trackSt)
	}
	if total <= pageH {
		return
	}
	thumbH := max(1, trackH*pageH/total)
	maxOff := total - pageH
	thumbTop := 0
	if maxOff > 0 {
		thumbTop = (trackH - thumbH) * scrollTop / maxOff
	}
	if thumbTop+thumbH > trackH {
		thumbTop = trackH - thumbH
	}
	for i := thumbTop; i < thumbTop+thumbH; i++ {
		screen.SetContent(x, y+i, thumbR, nil, thumbSt)
	}
}

// Draw renders the track and thumb.
func (sb *Scrollbar) Draw(screen tcell.Screen) {
	sb.Box.DrawForSubclass(screen, sb)
	x, y, _, h := sb.GetInnerRect()
	if h == 0 {
		return
	}

	total := sb.getTotal()
	scrollY := sb.getScrollY()
	pageH := sb.getPageH()

	scrolled := scrollY != sb.lastScrollY
	sb.lastScrollY = scrollY

	trackSt := tcell.StyleDefault.Background(Theme.Surface).Foreground(Theme.Muted)
	thumbColor := Theme.Border
	if sb.dragging || scrolled {
		thumbColor = Theme.Accent
	}
	thumbSt := tcell.StyleDefault.Background(thumbColor)

	drawScrollbarTrack(screen, x, y, h, total, pageH, scrollY, ' ', ' ', trackSt, thumbSt)
}

// MouseHandler handles left-click and drag to set scroll position.
func (sb *Scrollbar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, _ func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := event.Position()
		_, ry, _, rh := sb.GetRect()
		inRect := sb.InRect(mx, my)

		switch action {
		case tview.MouseLeftDown:
			if !inRect {
				return false, nil
			}
			sb.dragging = true
			sb.scrollToY(my-ry, rh)
			return true, sb // capture for drag

		case tview.MouseMove:
			if !sb.dragging {
				return false, nil
			}
			relY := my - ry
			if relY < 0 {
				relY = 0
			} else if relY >= rh {
				relY = rh - 1
			}
			sb.scrollToY(relY, rh)
			return true, sb

		case tview.MouseLeftUp:
			if !sb.dragging {
				return false, nil
			}
			sb.dragging = false
			return true, nil
		}
		return false, nil
	}
}

func (sb *Scrollbar) scrollToY(relY, trackH int) {
	total := sb.getTotal()
	pageH := sb.getPageH()
	if total <= pageH || trackH <= 1 {
		return
	}
	maxOff := total - pageH
	newOff := maxOff * relY / (trackH - 1)
	if newOff < 0 {
		newOff = 0
	} else if newOff > maxOff {
		newOff = maxOff
	}
	sb.scrollTo(newOff)
}
