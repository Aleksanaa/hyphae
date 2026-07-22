package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/strutil"
)

// DiffViewHeight is the fixed row count of the diff approval view when visible.
const DiffViewHeight = 18

// contentH is the number of diff rows visible at once.
const contentH = DiffViewHeight - 7

// diffNumPrefixW is the column width of the line-number + indicator prefix.
// Format: "OOOO NNNN I " = 4+1+4+1+1+1 = 12 columns.
const diffNumPrefixW = 12

// screenLine is one displayed row in the diff area.
type screenLine struct {
	dl             *DiffLine
	content        string
	isContinuation bool
}

// DiffFileChange is a single file being changed during a diff approval.
type DiffFileChange struct {
	Path  string
	Lines []DiffLine
}

// DiffView shows a syntax-highlighted unified diff with allow/deny buttons.
// Sits between chat and input; hidden (0 height) until shown.
type DiffView struct {
	*tview.Box
	toolName    string
	reasoning   string
	files       []DiffFileChange
	activeFile  int
	scrollTop   int
	visible     bool
	btnSelected string // "allow" | "deny"

	// deny text is managed by a native InputField.
	denyField *tview.InputField
	scrollbar *Scrollbar

	onAllow func()
	onDeny  func(string)

	cachedLines []screenLine
	cacheW      int
}

func NewDiffView() *DiffView {
	dv := &DiffView{Box: tview.NewBox(), btnSelected: "allow"}
	dv.Box.SetBackgroundColor(Theme.Surface)
	dv.SetBorder(true)
	dv.SetBorderColor(Theme.PendingColor)
	dv.SetTitleColor(Theme.PendingColor)
	dv.SetTitleAlign(tview.AlignLeft)
	dv.scrollbar = NewScrollbar(
		func() int { return len(dv.cachedLines) },
		func() int { return contentH },
		func() int { return dv.scrollTop },
		func(y int) { dv.scrollTop = y; dv.clampScroll() },
	)

	dv.denyField = tview.NewInputField()
	dv.denyField.SetPlaceholder("type reason here (optional)...")
	dv.denyField.SetFieldTextColor(Theme.Text)
	dv.denyField.SetFieldBackgroundColor(Theme.Surface)
	dv.denyField.SetBackgroundColor(Theme.Surface)
	dv.denyField.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			dv.confirm()
		}
	})

	return dv
}

// ── Focus delegation ─────────────────────────────────────────────────────────

func (dv *DiffView) Focus(delegate func(p tview.Primitive)) {
	dv.Box.Focus(delegate) // sets dv.Box.hasFocus = true
	if dv.btnSelected == "deny" {
		dv.denyField.Focus(func(tview.Primitive) {})
	} else {
		dv.denyField.Blur()
	}
}

// Restyle re-applies theme colors after a theme switch. Deny-field state colors
// are re-derived on the next render from the current mode.
func (dv *DiffView) Restyle() {
	dv.Box.SetBackgroundColor(Theme.Surface)
	dv.SetBorderColor(Theme.PendingColor)
	dv.SetTitleColor(Theme.PendingColor)
	dv.denyField.SetFieldTextColor(Theme.Text)
	dv.denyField.SetFieldBackgroundColor(Theme.Surface)
	dv.denyField.SetBackgroundColor(Theme.Surface)
}

// ── public API ───────────────────────────────────────────────────────────────

func (dv *DiffView) IsVisible() bool      { return dv.visible }
func (dv *DiffView) GetSelected() string  { return dv.btnSelected }
func (dv *DiffView) SetSelected(s string) { dv.btnSelected = s }

func (dv *DiffView) Show(toolName, reasoning string, files []DiffFileChange) {
	dv.toolName = toolName
	dv.SetTitle(" " + toolName + " ")
	dv.reasoning = reasoning
	dv.files = files
	dv.activeFile = 0
	dv.scrollTop = 0
	dv.visible = true
	dv.btnSelected = "allow"
	dv.denyField.SetText("")

	dv.cacheW = 0
	dv.cachedLines = nil
}

func (dv *DiffView) SetCallbacks(onAllow func(), onDeny func(string)) {
	dv.onAllow = onAllow
	dv.onDeny = onDeny
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
		dv.Deny(dv.denyField.GetText())
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
		for j, chunk := range tview.WordWrap(tview.Escape(content), codeW) {
			result = append(result, screenLine{
				dl:             dl,
				content:        tview.Unescape(chunk),
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

	bg := Theme.Surface
	borderSt := tcell.StyleDefault.Foreground(Theme.PendingColor).Background(bg)
	bgSt := tcell.StyleDefault.Background(bg)
	mutedSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(bg)

	leftT, rightT, horiz := tview.Borders.LeftT, tview.Borders.RightT, tview.Borders.Horizontal
	if dv.HasFocus() {
		leftT = tview.BoxDrawingsHeavyVerticalAndRight
		rightT = tview.BoxDrawingsHeavyVerticalAndLeft
		horiz = tview.BoxDrawingsHeavyHorizontal
	}

	inner := x + 2
	innerW := w - 4

	// ── row 1: file tabs ──────────────────────────────────────────────────
	col := inner
	for i, fc := range dv.files {
		isActive := i == dv.activeFile
		label := " " + strutil.Truncate(fc.Path, 40) + " "
		labelW := tview.TaggedStringWidth(tview.Escape(label))
		if col+labelW > inner+innerW {
			break
		}
		st := mutedSt
		if isActive {
			st = textSt
		}
		col += drawText(screen, label, col, y+1, inner+innerW-col, st)
		if i < len(dv.files)-1 && col < inner+innerW {
			screen.SetContent(col, y+1, '│', nil, mutedSt)
			col++
		}
	}

	// ── row 2: separator ─────────────────────────────────────────────────
	screen.SetContent(x, y+2, leftT, nil, borderSt)
	screen.SetContent(x+w-1, y+2, rightT, nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, y+2, horiz, nil, borderSt)
	}

	// ── rows 3..3+contentH-1: diff content ───────────────────────────────
	contentX := x + 1
	contentW := w - 3
	scrollbarX := x + w - 2

	if contentW != dv.cacheW {
		dv.cachedLines = dv.buildScreenLines(contentW)
		dv.cacheW = contentW
	}
	dv.clampScroll()
	filename := dv.currentFilename()

	for row := range contentH {
		rowY := y + 3 + row
		lineIdx := dv.scrollTop + row
		if lineIdx < len(dv.cachedLines) {
			dv.drawScreenLine(screen, &dv.cachedLines[lineIdx], rowY, contentX, contentW, filename)
		} else {
			for col := contentX; col < contentX+contentW; col++ {
				screen.SetContent(col, rowY, ' ', nil, bgSt)
			}
		}
	}

	dv.scrollbar.SetRect(scrollbarX, y+3, 1, contentH)
	dv.scrollbar.Draw(screen)

	// ── separator before footer ───────────────────────────────────────────
	sepY := y + 3 + contentH
	screen.SetContent(x, sepY, leftT, nil, borderSt)
	screen.SetContent(x+w-1, sepY, rightT, nil, borderSt)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, sepY, horiz, nil, borderSt)
	}

	// ── row sepY+1: reasoning ─────────────────────────────────────────────
	reasonY := sepY + 1
	if dv.reasoning != "" {
		col = inner
		used := drawText(screen, "reason: ", col, reasonY, innerW, mutedSt)
		col += used
		drawText(screen, strutil.Truncate(dv.reasoning, innerW-used), col, reasonY, innerW-used, textSt)
	}

	// ── row sepY+2: buttons ───────────────────────────────────────────────
	dv.drawButtons(screen, sepY+2, inner, innerW, bg)

}

func (dv *DiffView) drawButtons(screen tcell.Screen, y, inner, innerW int, bg tcell.Color) {
	allowSt := tcell.StyleDefault.Foreground(Theme.SuccessColor).Background(bg)
	if dv.btnSelected == "allow" {
		allowSt = tcell.StyleDefault.Background(approvalDarkGreen).Foreground(approvalWhite)
	}
	col := inner
	col += drawText(screen, "[ Allow ]", col, y, innerW, allowSt)

	col = inner + 9 + 3
	denyEnd := inner + innerW
	denyW := denyEnd - col
	if denyW < 10 {
		return
	}

	denyBracketSt := tcell.StyleDefault.Foreground(Theme.ErrorColor).Background(bg)
	if dv.btnSelected == "deny" {
		denyBracketSt = tcell.StyleDefault.Background(approvalDarkRed).Foreground(approvalWhite)
	}

	const denyPfx = "[ Deny: "
	const denySfx = " ]"
	col += drawText(screen, denyPfx, col, y, denyW, denyBracketSt)

	textAreaW := denyEnd - col - len([]rune(denySfx))
	if textAreaW > 0 {
		if dv.btnSelected == "deny" {
			dv.denyField.SetFieldBackgroundColor(approvalDarkRed)
			dv.denyField.SetFieldTextColor(approvalWhite)
			dv.denyField.SetPlaceholderStyle(
				tcell.StyleDefault.Foreground(Theme.Muted).Background(approvalDarkRed))
		} else {
			dv.denyField.SetFieldBackgroundColor(bg)
			dv.denyField.SetFieldTextColor(Theme.Muted)
			dv.denyField.SetPlaceholderStyle(
				tcell.StyleDefault.Foreground(Theme.Muted).Background(bg))
		}
		dv.denyField.SetBackgroundColor(bg)
		dv.denyField.SetRect(col, y, textAreaW, 1)
		dv.denyField.Draw(screen)
		col += textAreaW
	}
	drawText(screen, denySfx, col, y, len([]rune(denySfx)), denyBracketSt)
}

func (dv *DiffView) drawScreenLine(screen tcell.Screen, sl *screenLine, rowY, x, w int, filename string) {
	dl := sl.dl
	var bg, numFg, indFg tcell.Color
	var indicator rune

	switch dl.Type {
	case DiffAdded:
		bg = diffAddedBg
		numFg = diffAddedFg
		indFg = Theme.SuccessColor
		indicator = '+'
	case DiffRemoved:
		bg = diffRemovedBg
		numFg = diffRemovedFg
		indFg = Theme.ErrorColor
		indicator = '-'
	case DiffHunkHeader:
		bg = diffHunkBg
		numFg = diffHunkFg
		indFg = diffHunkFg
		indicator = ' '
	default:
		bg = Theme.Surface
		numFg = Theme.Muted
		indFg = Theme.Muted
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

func fmtDiffNum(n int) string {
	if n < 0 {
		return "    "
	}
	return fmt.Sprintf("%4d", n)
}

// ── InputHandler ─────────────────────────────────────────────────────────────

func (dv *DiffView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return dv.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
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
			dv.denyField.Blur()
			setFocus(dv)
		case tcell.KeyRight:
			dv.btnSelected = "deny"
			dv.denyField.Focus(func(tview.Primitive) {})
			setFocus(dv)
		case tcell.KeyEnter:
			dv.confirm()
		default:
			if dv.btnSelected == "deny" {
				if h := dv.denyField.InputHandler(); h != nil {
					h(event, setFocus)
				}
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

		contentStartY := y + 3
		contentEndY := y + 3 + contentH - 1
		scrollbarX := x + w - 2
		if my >= contentStartY && my <= contentEndY {
			if mx == scrollbarX {
				h := dv.scrollbar.MouseHandler()
				return h(action, event, setFocus)
			}
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

		btnY := y + DiffViewHeight - 2
		if my != btnY {
			return false, nil
		}
		inner := x + 2
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
			dv.btnSelected = side
			setFocus(dv)
			return true, nil
		case tview.MouseLeftDoubleClick:
			dv.btnSelected = side
			dv.confirm()
			return true, nil
		}
		return false, nil
	})
}
