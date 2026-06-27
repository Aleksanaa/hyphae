package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Scrollbar is a 1-column primitive that reflects and controls a ChatView's scroll position.
type Scrollbar struct {
	*tview.Box
	chat        *ChatView
	dragging    bool
	lastScrollY int
}

// NewScrollbar creates a scrollbar linked to chat.
func NewScrollbar(chat *ChatView) *Scrollbar {
	return &Scrollbar{Box: tview.NewBox(), chat: chat}
}

// Draw renders the track and thumb.
func (sb *Scrollbar) Draw(screen tcell.Screen) {
	sb.Box.DrawForSubclass(screen, sb)
	x, y, _, h := sb.GetInnerRect()
	if h == 0 {
		return
	}

	total := sb.chat.TotalLines
	scrollY, _ := sb.chat.GetScrollOffset()
	_, _, _, pageH := sb.chat.GetInnerRect()

	scrolled := scrollY != sb.lastScrollY
	sb.lastScrollY = scrollY

	trackSt := tcell.StyleDefault.Background(Theme.Surface).Foreground(Theme.Muted)
	thumbColor := Theme.Border
	if sb.dragging || scrolled {
		thumbColor = Theme.Accent
	}
	thumbSt := tcell.StyleDefault.Background(thumbColor)

	for i := range h {
		screen.SetContent(x, y+i, ' ', nil, trackSt)
	}

	if total <= pageH || total == 0 {
		return
	}

	thumbH := max(1, h*pageH/total)
	maxOff := total - pageH
	thumbTop := 0
	if maxOff > 0 {
		thumbTop = (h - thumbH) * scrollY / maxOff
	}
	if thumbTop+thumbH > h {
		thumbTop = h - thumbH
	}

	for i := thumbTop; i < thumbTop+thumbH; i++ {
		screen.SetContent(x, y+i, ' ', nil, thumbSt)
	}
}

// MouseHandler handles left-click and drag to set scroll position.
func (sb *Scrollbar) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, event *tcell.EventMouse, _ func(tview.Primitive)) (bool, tview.Primitive) {
		mx, my := event.Position()
		rx, ry, rw, rh := sb.GetRect()
		inRect := mx >= rx && mx < rx+rw && my >= ry && my < ry+rh

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
	total := sb.chat.TotalLines
	_, _, _, pageH := sb.chat.GetInnerRect()
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
	sb.chat.ScrollTo(newOff, 0)
}
