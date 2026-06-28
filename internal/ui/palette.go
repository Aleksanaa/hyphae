package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// paletteMode controls what the palette is showing.
type paletteMode int

const (
	paletteModeMenu        paletteMode = iota // top-level command list
	paletteModeAddEndpoint                    // form: name + base_url + api_key
	paletteModeDelEndpoint                    // list of endpoints to delete
	paletteModeSelectModel                    // list of models to pick
)

// PaletteItem is one selectable row in the palette list.
type PaletteItem struct {
	Label    string
	Sub      string // dim secondary text
	Value    string // opaque payload
	Action   func() // called on Enter (optional override)
}

// CommandPalette is a VS-Code-style Ctrl+P overlay drawn as a centered box.
type CommandPalette struct {
	*tview.Box
	visible bool
	mode    paletteMode

	// shared
	query     []rune
	cursor    int // rune index in query
	menuItems []PaletteItem // top-level items with actions, set by caller
	items     []PaletteItem
	filtered  []int // indices into items
	sel       int   // index into filtered

	// add-endpoint form
	formField int // 0=name 1=baseURL 2=apiKey
	formName  string
	formURL   string
	formKey   string

	// callbacks set by App
	onClose       func()
	onAddEndpoint func(name, baseURL, apiKey string)
	onDelEndpoint func(name string)
	onSelectModel func(model string)
	getEndpoints  func() []paletteEndpointInfo
}

type paletteEndpointInfo struct {
	Name    string
	BaseURL string
}

func NewCommandPalette() *CommandPalette {
	cp := &CommandPalette{Box: tview.NewBox()}
	return cp
}

// ── public API ────────────────────────────────────────────────────────────────

func (cp *CommandPalette) IsVisible() bool { return cp.visible }

func (cp *CommandPalette) Open() {
	cp.visible = true
	cp.mode = paletteModeMenu
	cp.query = cp.query[:0]
	cp.cursor = 0
	cp.sel = 0
	cp.items = cp.menuItems
	cp.refilter()
}

func (cp *CommandPalette) Close() {
	cp.visible = false
	if cp.onClose != nil {
		cp.onClose()
	}
}

func (cp *CommandPalette) SetCallbacks(
	onClose func(),
	onAddEndpoint func(name, baseURL, apiKey string),
	onDelEndpoint func(name string),
	onSelectModel func(model string),
	getEndpoints func() []paletteEndpointInfo,
) {
	cp.onClose = onClose
	cp.onAddEndpoint = onAddEndpoint
	cp.onDelEndpoint = onDelEndpoint
	cp.onSelectModel = onSelectModel
	cp.getEndpoints = getEndpoints
}

// ── drawing ───────────────────────────────────────────────────────────────────

// paletteW/H are the palette dimensions (capped to screen).
const (
	paletteW    = 64
	paletteMinH = 10
	paletteMaxH = 24
)

func (cp *CommandPalette) Draw(screen tcell.Screen) {
	if !cp.visible {
		return
	}
	sw, sh := screen.Size()

	w := paletteW
	if w > sw-4 {
		w = sw - 4
	}

	// Compute height: title+border(1) + query row(1) + divider(1) + items + bottom border(1)
	visItems := len(cp.filtered)
	if cp.mode == paletteModeAddEndpoint {
		visItems = 0 // form takes the item area
	}
	h := 4 + visItems
	if cp.mode == paletteModeAddEndpoint {
		h = 4 + 3 // 3 form fields
	}
	if h < paletteMinH {
		h = paletteMinH
	}
	if h > paletteMaxH {
		h = paletteMaxH
	}
	if h > sh-4 {
		h = sh - 4
	}

	x := (sw - w) / 2
	y := (sh - h) / 4 // slightly above center

	bc := Theme.BorderFocus
	mc := Theme.Muted
	ac := Theme.Accent
	tc := Theme.Text
	bg := Theme.Surface

	borderSt := tcell.StyleDefault.Foreground(bc).Background(bg)
	mutedSt := tcell.StyleDefault.Foreground(mc).Background(bg)
	textSt := tcell.StyleDefault.Foreground(tc).Background(bg)
	accentSt := tcell.StyleDefault.Foreground(ac).Background(bg)
	bgSt := tcell.StyleDefault.Background(bg)
	selSt := tcell.StyleDefault.Background(tcell.NewRGBColor(40, 44, 70)).Foreground(tc)
	selAccSt := tcell.StyleDefault.Background(tcell.NewRGBColor(40, 44, 70)).Foreground(ac)

	// Fill background.
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			screen.SetContent(x+col, y+row, ' ', nil, bgSt)
		}
	}

	// Top border with title.
	title := cp.modeTitle()
	labelW := len([]rune(title)) + 2 // " title "
	fill := w - 2 - labelW
	if fill < 0 {
		fill = 0
	}
	screen.SetContent(x, y, '┌', nil, borderSt)
	for i := 0; i < fill; i++ {
		screen.SetContent(x+1+i, y, '─', nil, borderSt)
	}
	screen.SetContent(x+1+fill, y, ' ', nil, borderSt)
	for i, r := range []rune(title) {
		screen.SetContent(x+2+fill+i, y, r, nil, accentSt)
	}
	screen.SetContent(x+2+fill+len([]rune(title)), y, ' ', nil, borderSt)
	screen.SetContent(x+w-1, y, '┐', nil, borderSt)

	// Query / form row.
	screen.SetContent(x, y+1, '│', nil, borderSt)
	screen.SetContent(x+w-1, y+1, '│', nil, borderSt)
	if cp.mode == paletteModeAddEndpoint {
		cp.drawFormRow(screen, x, y, w, bg, mutedSt, textSt)
	} else {
		cp.drawQueryRow(screen, x, y, w, mutedSt, textSt)
	}

	// Divider.
	screen.SetContent(x, y+2, '├', nil, borderSt)
	for col := 1; col < w-1; col++ {
		screen.SetContent(x+col, y+2, '─', nil, borderSt)
	}
	screen.SetContent(x+w-1, y+2, '┤', nil, borderSt)

	// Items area.
	itemsH := h - 4 // rows between divider and bottom border
	if cp.mode == paletteModeAddEndpoint {
		cp.drawForm(screen, x, y+3, w, itemsH, bg, mutedSt, textSt, accentSt, borderSt)
	} else {
		cp.drawItems(screen, x, y+3, w, itemsH, bgSt, selSt, selAccSt, mutedSt, textSt, accentSt)
	}
	// Side borders for item rows.
	for row := 3; row < h-1; row++ {
		screen.SetContent(x, y+row, '│', nil, borderSt)
		screen.SetContent(x+w-1, y+row, '│', nil, borderSt)
	}

	// Bottom border.
	screen.SetContent(x, y+h-1, '└', nil, borderSt)
	for col := 1; col < w-1; col++ {
		screen.SetContent(x+col, y+h-1, '─', nil, borderSt)
	}
	screen.SetContent(x+w-1, y+h-1, '┘', nil, borderSt)
}

func (cp *CommandPalette) drawQueryRow(screen tcell.Screen, x, y, w int, mutedSt, textSt tcell.Style) {
	inner := x + 2
	innerW := w - 4
	prompt := []rune("> ")
	col := inner
	for _, r := range prompt {
		screen.SetContent(col, y+1, r, nil, mutedSt)
		col++
	}
	maxQ := innerW - len(prompt)
	q := cp.query
	viewStart := 0
	if len(q) > 0 && cp.cursor >= maxQ {
		viewStart = cp.cursor - maxQ + 1
	}
	cursorSt := tcell.StyleDefault.Background(Theme.Text).Foreground(Theme.Surface)
	for i := 0; i < maxQ; i++ {
		ri := viewStart + i
		var r rune = ' '
		st := textSt
		if ri == cp.cursor {
			st = cursorSt
			if ri < len(q) {
				r = q[ri]
			}
		} else if ri < len(q) {
			r = q[ri]
		}
		screen.SetContent(col, y+1, r, nil, st)
		col++
	}
}

func (cp *CommandPalette) drawFormRow(screen tcell.Screen, x, y, w int, bg tcell.Color, mutedSt, textSt tcell.Style) {
	hint := "fill in fields below, Enter to confirm"
	inner := x + 2
	col := inner
	for _, r := range []rune(hint) {
		if col >= x+w-1 {
			break
		}
		screen.SetContent(col, y+1, r, nil, mutedSt)
		col++
	}
	_ = bg
}

func (cp *CommandPalette) drawForm(screen tcell.Screen, x, y, w, h int, bg tcell.Color, mutedSt, textSt, accentSt, borderSt tcell.Style) {
	labels := []string{"name     ", "base url ", "api key  "}
	values := []*string{&cp.formName, &cp.formURL, &cp.formKey}
	inner := x + 2
	innerW := w - 4
	cursorSt := tcell.StyleDefault.Background(Theme.Text).Foreground(Theme.Surface)
	activeSt := tcell.StyleDefault.Foreground(Theme.Accent).Background(bg)

	for i, label := range labels {
		if i >= h {
			break
		}
		row := y + i
		labelRunes := []rune(label + "❯ ")
		col := inner
		st := mutedSt
		if i == cp.formField {
			st = activeSt
		}
		for _, r := range labelRunes {
			if col >= x+w-1 {
				break
			}
			screen.SetContent(col, row, r, nil, st)
			col++
		}
		maxV := innerW - len(labelRunes)
		val := []rune(*values[i])
		for j := 0; j < maxV; j++ {
			var r rune = ' '
			cellSt := textSt
			if i == cp.formField && j == len(val) {
				cellSt = cursorSt
			} else if j < len(val) {
				r = val[j]
			}
			screen.SetContent(col, row, r, nil, cellSt)
			col++
		}
	}
	_ = borderSt
	_ = accentSt
}

func (cp *CommandPalette) drawItems(screen tcell.Screen, x, y, w, h int, bgSt, selSt, selAccSt, mutedSt, textSt, accentSt tcell.Style) {
	inner := x + 2
	innerW := w - 4

	// Scroll so sel is visible.
	offset := 0
	if cp.sel >= h {
		offset = cp.sel - h + 1
	}

	for row := 0; row < h; row++ {
		fi := offset + row
		rowY := y + row
		if fi >= len(cp.filtered) {
			break
		}
		item := cp.items[cp.filtered[fi]]
		isSel := fi == cp.sel

		lineSt := textSt
		subSt := mutedSt
		if isSel {
			lineSt = selSt
			subSt = selSt
			// fill selection background across full width
			for col := 1; col < w-1; col++ {
				screen.SetContent(x+col, rowY, ' ', nil, selSt)
			}
		}
		_ = accentSt
		_ = selAccSt

		col := inner
		label := []rune(item.Label)
		for i, r := range label {
			if col >= x+w-1 {
				break
			}
			_ = i
			screen.SetContent(col, rowY, r, nil, lineSt)
			col++
		}
		if item.Sub != "" {
			subRunes := []rune("  " + item.Sub)
			remaining := innerW - len(label)
			for _, r := range subRunes {
				if col >= x+w-1 || remaining <= 0 {
					break
				}
				screen.SetContent(col, rowY, r, nil, subSt)
				col++
				remaining--
			}
		}
	}
}

// ── input ─────────────────────────────────────────────────────────────────────

func (cp *CommandPalette) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return cp.WrapInputHandler(func(event *tcell.EventKey, _ func(tview.Primitive)) {
		if !cp.visible {
			return
		}
		switch event.Key() {
		case tcell.KeyEscape:
			if cp.mode != paletteModeMenu {
				cp.switchMode(paletteModeMenu)
			} else {
				cp.Close()
			}

		case tcell.KeyEnter:
			cp.confirm()

		case tcell.KeyUp:
			if cp.mode != paletteModeAddEndpoint {
				if cp.sel > 0 {
					cp.sel--
				}
			} else {
				if cp.formField > 0 {
					cp.formField--
				}
			}

		case tcell.KeyDown:
			if cp.mode != paletteModeAddEndpoint {
				if cp.sel < len(cp.filtered)-1 {
					cp.sel++
				}
			} else {
				if cp.formField < 2 {
					cp.formField++
				}
			}

		case tcell.KeyTab:
			if cp.mode == paletteModeAddEndpoint {
				cp.formField = (cp.formField + 1) % 3
			}

		case tcell.KeyBackspace, tcell.KeyBackspace2:
			if cp.mode == paletteModeAddEndpoint {
				cp.formBackspace()
			} else if cp.cursor > 0 {
				cp.query = append(cp.query[:cp.cursor-1], cp.query[cp.cursor:]...)
				cp.cursor--
				cp.refilter()
			}

		default:
			if event.Rune() >= 32 {
				if cp.mode == paletteModeAddEndpoint {
					cp.formInsert(event.Rune())
				} else {
					cp.query = append(cp.query[:cp.cursor], append([]rune{event.Rune()}, cp.query[cp.cursor:]...)...)
					cp.cursor++
					cp.refilter()
				}
			}
		}
	})
}

func (cp *CommandPalette) formBackspace() {
	ptr := cp.formActivePtr()
	r := []rune(*ptr)
	if len(r) > 0 {
		*ptr = string(r[:len(r)-1])
	}
}

func (cp *CommandPalette) formInsert(ch rune) {
	ptr := cp.formActivePtr()
	*ptr += string(ch)
}

func (cp *CommandPalette) formActivePtr() *string {
	switch cp.formField {
	case 0:
		return &cp.formName
	case 1:
		return &cp.formURL
	default:
		return &cp.formKey
	}
}

func (cp *CommandPalette) confirm() {
	switch cp.mode {
	case paletteModeMenu:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if item.Action != nil {
			item.Action()
		}

	case paletteModeAddEndpoint:
		name := strings.TrimSpace(cp.formName)
		baseURL := strings.TrimSpace(cp.formURL)
		apiKey := strings.TrimSpace(cp.formKey)
		if name == "" || baseURL == "" || apiKey == "" {
			return
		}
		if cp.onAddEndpoint != nil {
			cp.onAddEndpoint(name, baseURL, apiKey)
		}
		cp.Close()

	case paletteModeDelEndpoint:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if cp.onDelEndpoint != nil {
			cp.onDelEndpoint(item.Value)
		}
		cp.Close()

	case paletteModeSelectModel:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if cp.onSelectModel != nil {
			cp.onSelectModel(item.Value)
		}
		cp.Close()
	}
}

// ── mode switching ────────────────────────────────────────────────────────────

func (cp *CommandPalette) switchMode(m paletteMode) {
	cp.mode = m
	cp.query = cp.query[:0]
	cp.cursor = 0
	cp.sel = 0

	switch m {
	case paletteModeMenu:
		cp.items = cp.menuItems

	case paletteModeAddEndpoint:
		cp.formField = 0
		cp.formName = ""
		cp.formURL = ""
		cp.formKey = ""
		cp.items = nil

	case paletteModeDelEndpoint:
		eps := cp.getEndpoints()
		cp.items = make([]PaletteItem, len(eps))
		for i, ep := range eps {
			cp.items[i] = PaletteItem{Label: ep.Name, Sub: ep.BaseURL, Value: ep.Name}
		}

	case paletteModeSelectModel:
		cp.items = []PaletteItem{{Label: "fetching models…"}}
	}
	cp.refilter()
}

func topLevelItems() []PaletteItem {
	return []PaletteItem{
		{Label: "Add endpoint", Sub: "register a new API endpoint"},
		{Label: "Delete endpoint", Sub: "remove a saved endpoint"},
		{Label: "Select model", Sub: "choose model from an endpoint"},
	}
}

func (cp *CommandPalette) modeTitle() string {
	switch cp.mode {
	case paletteModeAddEndpoint:
		return "add endpoint"
	case paletteModeDelEndpoint:
		return "delete endpoint"
	case paletteModeSelectModel:
		return "select model"
	default:
		return "command palette"
	}
}

func (cp *CommandPalette) refilter() {
	q := strings.ToLower(string(cp.query))
	cp.filtered = cp.filtered[:0]
	for i, item := range cp.items {
		if q == "" || strings.Contains(strings.ToLower(item.Label), q) || strings.Contains(strings.ToLower(item.Sub), q) {
			cp.filtered = append(cp.filtered, i)
		}
	}
	if cp.sel >= len(cp.filtered) {
		cp.sel = max(0, len(cp.filtered)-1)
	}
}

// SetModelItems replaces the item list in select-model mode (called after fetch).
func (cp *CommandPalette) SetModelItems(items []PaletteItem) {
	cp.items = items
	cp.refilter()
}

