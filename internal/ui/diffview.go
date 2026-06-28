package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// DiffViewHeight is the fixed row count of the diff approval view when visible.
const DiffViewHeight = 18

// contentH is the number of diff rows visible at once.
const contentH = DiffViewHeight - 7

// diffNumPrefixW is the column width of the line-number + indicator prefix.
// Format: "OOOO NNNN I " = 4+1+4+1+1+1 = 12 columns.
const diffNumPrefixW = 12

// screenLine is one displayed row in the diff area. A logical DiffLine may
// expand into multiple screenLines when it is wider than the available width.
type screenLine struct {
	dl             *DiffLine // logical diff line this belongs to
	content        string    // tab-expanded content chunk for this screen row
	isContinuation bool      // true → blank line numbers (wrapped continuation)
}

// Diff background / foreground palette — kept local to avoid polluting Theme.
var (
	diffAddedBg   = tcell.NewRGBColor(15, 50, 15)
	diffRemovedBg = tcell.NewRGBColor(55, 15, 15)
	diffHunkBg    = tcell.NewRGBColor(22, 28, 50)
	diffAddedFg   = tcell.NewRGBColor(80, 180, 80)
	diffRemovedFg = tcell.NewRGBColor(210, 90, 90)
	diffHunkFg    = tcell.NewRGBColor(120, 145, 210)
)

// DiffFileChange is a single file being changed during a diff approval.
type DiffFileChange struct {
	Path  string
	Lines []DiffLine
}

// DiffView is a custom-drawn approval widget that shows a syntax-highlighted
// unified diff. Sits between chat and input; hidden (0 height) until shown.
type DiffView struct {
	*tview.Box
	toolName      string
	reasoning     string
	files         []DiffFileChange
	activeFile    int
	scrollTop     int
	visible       bool
	btnSelected   string // "allow" | "deny"
	denyText      string
	denyCursor    int
	lastClickSide string
	lastClickTime time.Time
	onAllow       func()
	onDeny        func(string)
	// Wrapped screen-line cache, rebuilt when content width changes.
	cachedLines []screenLine
	cacheW      int
}

func NewDiffView() *DiffView {
	return &DiffView{Box: tview.NewBox(), btnSelected: "allow"}
}

func (dv *DiffView) IsVisible() bool      { return dv.visible }
func (dv *DiffView) GetSelected() string  { return dv.btnSelected }
func (dv *DiffView) SetSelected(s string) { dv.btnSelected = s }

// Show populates the view and makes it visible.
func (dv *DiffView) Show(toolName, reasoning string, files []DiffFileChange) {
	dv.toolName      = toolName
	dv.reasoning     = reasoning
	dv.files         = files
	dv.activeFile    = 0
	dv.scrollTop     = 0
	dv.visible       = true
	dv.btnSelected   = "allow"
	dv.denyText      = ""
	dv.denyCursor    = 0
	dv.lastClickSide = ""
	dv.cacheW        = 0 // invalidate screen-line cache
	dv.cachedLines   = nil
}

func (dv *DiffView) SetCallbacks(onAllow func(), onDeny func(string)) {
	dv.onAllow = onAllow
	dv.onDeny  = onDeny
}

func (dv *DiffView) Allow() {
	if dv.onAllow != nil {
		dv.onAllow()
	}
}

func (dv *DiffView) Deny(reason string) {
	if dv.onDeny != nil {
		dv.onDeny(reason)
	}
}

func (dv *DiffView) confirm() {
	if dv.btnSelected == "allow" {
		dv.Allow()
	} else {
		dv.Deny(dv.denyText)
	}
}

func (dv *DiffView) currentLines() []DiffLine {
	if dv.activeFile < len(dv.files) {
		return dv.files[dv.activeFile].Lines
	}
	return nil
}

func (dv *DiffView) currentFilename() string {
	if dv.activeFile < len(dv.files) {
		return dv.files[dv.activeFile].Path
	}
	return ""
}

func (dv *DiffView) clampScroll() {
	maxTop := len(dv.cachedLines) - contentH
	if maxTop < 0 {
		maxTop = 0
	}
	if dv.scrollTop < 0 {
		dv.scrollTop = 0
	} else if dv.scrollTop > maxTop {
		dv.scrollTop = maxTop
	}
}

// buildScreenLines expands logical DiffLines into wrapped screen rows using the
// same word-wrap logic as chat messages (wrapParagraph).
// contentW is the column width of the diff content area (excluding scrollbar).
func (dv *DiffView) buildScreenLines(contentW int) []screenLine {
	codeW := contentW - diffNumPrefixW
	if codeW < 4 {
		codeW = 4
	}
	diffLines := dv.currentLines()
	var result []screenLine
	for i := range diffLines {
		dl := &diffLines[i]
		if dl.Type == DiffHunkHeader {
			result = append(result, screenLine{dl: dl, content: dl.Content})
			continue
		}
		content := strings.ReplaceAll(dl.Content, "\t", "    ")
		for j, chunk := range wrapParagraph(content, codeW) {
			result = append(result, screenLine{
				dl:             dl,
				content:        chunk,
				isContinuation: j > 0,
			})
		}
	}
	return result
}

// ── Draw ─────────────────────────────────────────────────────────────────────

func (dv *DiffView) Draw(screen tcell.Screen) {
	dv.Box.DrawForSubclass(screen, dv)
	if !dv.visible {
		return
	}
	x, y, w, _ := dv.GetRect()
	if w < 30 {
		return
	}
	h := DiffViewHeight

	pending  := Theme.PendingColor
	borderSt := tcell.StyleDefault.Foreground(pending)
	bgSt     := tcell.StyleDefault.Background(Theme.Surface)
	mutedSt  := tcell.StyleDefault.Foreground(Theme.Muted).Background(Theme.Surface)
	textSt   := tcell.StyleDefault.Foreground(Theme.Text).Background(Theme.Surface)

	// Fill all rows with Surface background.
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			screen.SetContent(x+col, y+row, ' ', nil, bgSt)
		}
	}

	// ── row 0: top border ┌─ toolname ──────────────┐ ─────────────────────
	screen.SetContent(x, y, '┌', nil, borderSt)
	screen.SetContent(x+w-1, y, '┐', nil, borderSt)
	col := x + 1
	screen.SetContent(col, y, '─', nil, borderSt)
	col++
	screen.SetContent(col, y, ' ', nil, borderSt)
	col++
	toolSt := tcell.StyleDefault.Foreground(Theme.PendingColor)
	for _, r := range []rune(dv.toolName) {
		screen.SetContent(col, y, r, nil, toolSt)
		col++
	}
	screen.SetContent(col, y, ' ', nil, borderSt)
	col++
	for ; col < x+w-1; col++ {
		screen.SetContent(col, y, '─', nil, borderSt)
	}

	// ── side borders (rows 1..h-2) ────────────────────────────────────────
	for row := 1; row < h-1; row++ {
		screen.SetContent(x, y+row, '│', nil, borderSt)
		screen.SetContent(x+w-1, y+row, '│', nil, borderSt)
	}

	inner  := x + 2
	innerW := w - 4

	// ── row 1: file tabs ──────────────────────────────────────────────────
	col = inner
	for i, fc := range dv.files {
		isActive := i == dv.activeFile
		label := []rune(" " + truncateStr(fc.Path, 40) + " ")
		var st tcell.Style
		if isActive {
			st = tcell.StyleDefault.Foreground(Theme.Text).Background(Theme.Surface)
		} else {
			st = mutedSt
		}
		if col+len(label) > inner+innerW {
			break
		}
		for _, r := range label {
			screen.SetContent(col, y+1, r, nil, st)
			col++
		}
		if isActive {
			// Underline the active tab by re-drawing with underline
			// (draw bracket highlights instead since underline is terminal-dependent)
		}
		if i < len(dv.files)-1 && col < inner+innerW {
			screen.SetContent(col, y+1, '│', nil, mutedSt)
			col++
		}
	}

	// ── row 2: separator ─────────────────────────────────────────────────
	screen.SetContent(x, y+2, '├', nil, borderSt)
	screen.SetContent(x+w-1, y+2, '┤', nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, y+2, '─', nil, borderSt)
	}

	// ── rows 3..3+contentH-1: diff content ───────────────────────────────
	// Content occupies cols x+1..x+w-3; scrollbar at x+w-2.
	contentX   := x + 1
	contentW   := w - 3
	scrollbarX := x + w - 2

	// Rebuild wrapped screen-line cache when width changes.
	if contentW != dv.cacheW {
		dv.cachedLines = dv.buildScreenLines(contentW)
		dv.cacheW = contentW
	}
	dv.clampScroll()
	filename := dv.currentFilename()

	for row := 0; row < contentH; row++ {
		rowY    := y + 3 + row
		lineIdx := dv.scrollTop + row
		if lineIdx < len(dv.cachedLines) {
			dv.drawScreenLine(screen, &dv.cachedLines[lineIdx], rowY, contentX, contentW, filename)
		} else {
			for col := contentX; col < contentX+contentW; col++ {
				screen.SetContent(col, rowY, ' ', nil, bgSt)
			}
		}
	}

	dv.drawScrollbar(screen, scrollbarX, y+3, contentH, len(dv.cachedLines), dv.scrollTop)

	// ── separator before footer ───────────────────────────────────────────
	sepY := y + 3 + contentH
	screen.SetContent(x, sepY, '├', nil, borderSt)
	screen.SetContent(x+w-1, sepY, '┤', nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, sepY, '─', nil, borderSt)
	}

	// ── row sepY+1: reasoning ─────────────────────────────────────────────
	reasonY := sepY + 1
	if dv.reasoning != "" {
		col = inner
		for _, r := range []rune("reason: ") {
			if col >= inner+innerW {
				break
			}
			screen.SetContent(col, reasonY, r, nil, mutedSt)
			col++
		}
		maxR := inner + innerW - col
		for _, r := range []rune(truncateStr(dv.reasoning, maxR)) {
			if col >= inner+innerW {
				break
			}
			screen.SetContent(col, reasonY, r, nil, textSt)
			col++
		}
	}

	// ── row sepY+2: buttons ───────────────────────────────────────────────
	dv.drawButtons(screen, sepY+2, inner, innerW)

	// ── bottom border ─────────────────────────────────────────────────────
	botY := y + h - 1
	screen.SetContent(x, botY, '└', nil, borderSt)
	screen.SetContent(x+w-1, botY, '┘', nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, botY, '─', nil, borderSt)
	}
}

func (dv *DiffView) drawScreenLine(screen tcell.Screen, sl *screenLine, rowY, x, w int, filename string) {
	dl := sl.dl
	var bg, numFg, indFg tcell.Color
	var indicator rune

	switch dl.Type {
	case DiffAdded:
		bg        = diffAddedBg
		numFg     = diffAddedFg
		indFg     = Theme.SuccessColor
		indicator = '+'
	case DiffRemoved:
		bg        = diffRemovedBg
		numFg     = diffRemovedFg
		indFg     = Theme.ErrorColor
		indicator = '-'
	case DiffHunkHeader:
		bg        = diffHunkBg
		numFg     = diffHunkFg
		indFg     = diffHunkFg
		indicator = ' '
	default:
		bg        = Theme.Surface
		numFg     = Theme.Muted
		indFg     = Theme.Muted
		indicator = ' '
	}

	bgSt := tcell.StyleDefault.Background(bg)
	for col := x; col < x+w; col++ {
		screen.SetContent(col, rowY, ' ', nil, bgSt)
	}

	if dl.Type == DiffHunkHeader {
		hunkSt := tcell.StyleDefault.Foreground(diffHunkFg).Background(diffHunkBg)
		col := x
		for _, r := range []rune("    ···  " + sl.content) {
			cw := tview.TaggedStringWidth(string(r))
			if cw == 0 {
				cw = 1
			}
			if col+cw > x+w {
				break
			}
			screen.SetContent(col, rowY, r, nil, hunkSt)
			col += cw
		}
		return
	}

	numSt := tcell.StyleDefault.Foreground(numFg).Background(bg)
	col := x

	if sl.isContinuation {
		// Blank prefix — same width as line-number area, bg-colored.
		for i := 0; i < diffNumPrefixW && col < x+w; i++ {
			screen.SetContent(col, rowY, ' ', nil, bgSt)
			col++
		}
	} else {
		for _, r := range []rune(fmtDiffNum(dl.OldNum)) {
			if col >= x+w {
				break
			}
			screen.SetContent(col, rowY, r, nil, numSt)
			col++
		}
		if col < x+w {
			screen.SetContent(col, rowY, ' ', nil, bgSt)
			col++
		}
		for _, r := range []rune(fmtDiffNum(dl.NewNum)) {
			if col >= x+w {
				break
			}
			screen.SetContent(col, rowY, r, nil, numSt)
			col++
		}
		if col < x+w {
			screen.SetContent(col, rowY, ' ', nil, bgSt)
			col++
		}
		indSt := tcell.StyleDefault.Foreground(indFg).Background(bg)
		if col < x+w {
			screen.SetContent(col, rowY, indicator, nil, indSt)
			col++
		}
		if col < x+w {
			screen.SetContent(col, rowY, ' ', nil, bgSt)
			col++
		}
	}

	// Content is already tab-expanded and width-chunked by buildScreenLines.
	for _, t := range tokenizeForTcell(sl.content, filename, bg) {
		cw := tview.TaggedStringWidth(string(t.R))
		if cw == 0 {
			cw = 1
		}
		if col+cw > x+w {
			break
		}
		screen.SetContent(col, rowY, t.R, nil, t.Style)
		col += cw
	}
}

func (dv *DiffView) drawScrollbar(screen tcell.Screen, x, y, h, total, scrollTop int) {
	trackSt := tcell.StyleDefault.Foreground(Theme.Border).Background(Theme.Surface)
	thumbSt := tcell.StyleDefault.Foreground(Theme.Accent).Background(Theme.Surface)
	if total <= h {
		for i := range h {
			screen.SetContent(x, y+i, '│', nil, trackSt)
		}
		return
	}
	thumbH := max(1, h*h/total)
	thumbTop := 0
	if total > h {
		thumbTop = scrollTop * (h - thumbH) / (total - h)
	}
	for i := range h {
		if i >= thumbTop && i < thumbTop+thumbH {
			screen.SetContent(x, y+i, '█', nil, thumbSt)
		} else {
			screen.SetContent(x, y+i, '│', nil, trackSt)
		}
	}
}

func (dv *DiffView) drawButtons(screen tcell.Screen, y, inner, innerW int) {
	white     := tcell.NewRGBColor(240, 240, 240)
	darkGreen := tcell.NewRGBColor(30, 90, 50)
	darkRed   := tcell.NewRGBColor(100, 35, 35)
	bgSt      := tcell.StyleDefault.Background(Theme.Surface)

	allowLabel := []rune("[ Allow ]")
	allowSt := tcell.StyleDefault.Foreground(Theme.SuccessColor).Background(Theme.Surface)
	if dv.btnSelected == "allow" {
		allowSt = tcell.StyleDefault.Background(darkGreen).Foreground(white)
	}
	col := inner
	for _, r := range allowLabel {
		screen.SetContent(col, y, r, nil, allowSt)
		col++
	}
	_ = bgSt

	col = inner + len(allowLabel) + 3
	denyEnd := inner + innerW
	denyW   := denyEnd - col
	if denyW < 10 {
		return
	}

	denyBracketSt := tcell.StyleDefault.Foreground(Theme.ErrorColor).Background(Theme.Surface)
	denyTextSt    := tcell.StyleDefault.Foreground(Theme.Text).Background(Theme.Surface)
	denyCursorSt  := tcell.StyleDefault.Background(Theme.Text).Foreground(Theme.Surface)
	placeholderSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(Theme.Surface)
	if dv.btnSelected == "deny" {
		denyBracketSt = tcell.StyleDefault.Background(darkRed).Foreground(white)
		denyTextSt    = tcell.StyleDefault.Background(darkRed).Foreground(white)
		denyCursorSt  = tcell.StyleDefault.Background(white).Foreground(darkRed)
		placeholderSt = tcell.StyleDefault.Background(darkRed).Foreground(Theme.Muted)
	}

	denyPrefix := []rune("[ Deny: ")
	denySuffix := []rune(" ]")
	textAreaW := denyW - len(denyPrefix) - len(denySuffix)
	if textAreaW < 0 {
		textAreaW = 0
	}
	for _, r := range denyPrefix {
		screen.SetContent(col, y, r, nil, denyBracketSt)
		col++
	}

	textRunes  := []rune(dv.denyText)
	placeholder := []rune("type reason here (optional)...")
	isEmpty := len(textRunes) == 0
	viewStart := 0
	if textAreaW > 0 && dv.denyCursor >= textAreaW {
		viewStart = dv.denyCursor - textAreaW + 1
	}
	for i := 0; i < textAreaW; i++ {
		ri := viewStart + i
		var r rune = ' '
		var st tcell.Style
		if isEmpty {
			if ri < len(placeholder) {
				r = placeholder[ri]
			}
			st = placeholderSt
			if dv.btnSelected == "deny" && ri == dv.denyCursor {
				st = denyCursorSt
			}
		} else {
			if ri < len(textRunes) {
				r = textRunes[ri]
			}
			st = denyTextSt
			if dv.btnSelected == "deny" && ri == dv.denyCursor {
				st = denyCursorSt
			}
		}
		screen.SetContent(col, y, r, nil, st)
		col++
	}
	for _, r := range denySuffix {
		screen.SetContent(col, y, r, nil, denyBracketSt)
		col++
	}
}

func fmtDiffNum(n int) string {
	if n < 0 {
		return "    "
	}
	return fmt.Sprintf("%4d", n)
}

// ── InputHandler ─────────────────────────────────────────────────────────────

func (dv *DiffView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return dv.WrapInputHandler(func(event *tcell.EventKey, _ func(tview.Primitive)) {
		if !dv.visible {
			return
		}
		switch event.Key() {
		case tcell.KeyUp:
			dv.scrollTop--
			dv.clampScroll()
		case tcell.KeyDown:
			dv.scrollTop++
			dv.clampScroll()
		case tcell.KeyPgUp:
			dv.scrollTop -= contentH
			dv.clampScroll()
		case tcell.KeyPgDn:
			dv.scrollTop += contentH
			dv.clampScroll()
		case tcell.KeyLeft:
			dv.btnSelected = "allow"
		case tcell.KeyRight:
			dv.btnSelected = "deny"
		case tcell.KeyEnter:
			dv.confirm()
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if dv.btnSelected == "deny" && dv.denyCursor > 0 {
				r := []rune(dv.denyText)
				dv.denyText = string(append(r[:dv.denyCursor-1], r[dv.denyCursor:]...))
				dv.denyCursor--
			}
		case tcell.KeyDelete:
			if dv.btnSelected == "deny" {
				r := []rune(dv.denyText)
				if dv.denyCursor < len(r) {
					dv.denyText = string(append(r[:dv.denyCursor], r[dv.denyCursor+1:]...))
				}
			}
		default:
			if dv.btnSelected == "deny" && event.Rune() >= 32 {
				r := []rune(dv.denyText)
				n := make([]rune, len(r)+1)
				copy(n, r[:dv.denyCursor])
				n[dv.denyCursor] = event.Rune()
				copy(n[dv.denyCursor+1:], r[dv.denyCursor:])
				dv.denyText = string(n)
				dv.denyCursor++
			}
		}
	})
}

// ── MouseHandler ─────────────────────────────────────────────────────────────

func (dv *DiffView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return dv.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !dv.visible {
			return false, nil
		}
		mx, my := event.Position()
		x, y, w, _ := dv.GetRect()

		// Scroll within content area
		contentStartY := y + 3
		contentEndY   := y + 3 + contentH - 1
		if my >= contentStartY && my <= contentEndY {
			switch action {
			case tview.MouseScrollUp:
				dv.scrollTop--
				dv.clampScroll()
				return true, nil
			case tview.MouseScrollDown:
				dv.scrollTop++
				dv.clampScroll()
				return true, nil
			}
		}

		// Button row
		btnY := y + DiffViewHeight - 2
		if my != btnY {
			return false, nil
		}
		inner    := x + 2
		allowEnd := inner + 9
		denyStart := inner + 12

		var side string
		switch {
		case mx >= inner && mx < allowEnd:
			side = "allow"
		case mx >= denyStart && mx < x+w-2:
			side = "deny"
		default:
			return false, nil
		}

		switch action {
		case tview.MouseLeftDown:
			setFocus(dv)
			return true, nil
		case tview.MouseLeftClick:
			now := time.Now()
			if side == dv.lastClickSide && now.Sub(dv.lastClickTime) < 400*time.Millisecond {
				dv.btnSelected = side
				dv.confirm()
			} else {
				dv.btnSelected = side
				dv.lastClickSide = side
				dv.lastClickTime = now
			}
			return true, nil
		}
		return false, nil
	})
}
