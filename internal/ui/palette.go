package ui

import (
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// paletteMode controls what the palette is showing.
type paletteMode int

const (
	paletteModeMenu          paletteMode = iota // top-level command list
	paletteModeAddEndpoint                      // form: name + base_url + api_key
	paletteModeDelEndpoint                      // list of endpoints to delete
	paletteModeSelectModel                      // list of models to pick
	paletteModeResumeSession                    // list of past sessions to resume
	paletteModeHotkeys                          // list of keyboard shortcuts
	paletteModeSelectTheme                      // list of color themes to pick
)

// PaletteItem is one selectable entry in the palette list. An entry occupies one
// row normally, or two when Detail is set (Detail renders as a dim second line).
type PaletteItem struct {
	Label  string // primary line, left-aligned (supports color tags)
	Sub    string // dim secondary text shown right-aligned on the primary line
	Detail string // optional dim second line; empty means a single-row entry
	Value  string // opaque payload passed to callbacks
	Action func() // called on Enter in menu mode
}

type paletteEndpointInfo struct {
	Name    string
	BaseURL string
}

type paletteSessionInfo struct {
	ID        string
	Title     string
	UpdatedAt int64
}

// CommandPalette is a VS-Code-style Ctrl+P overlay drawn as a centered box.
// Text input is handled by embedded tview.InputField instances so that cursor
// rendering, CJK input, and wide-char layout are all native to tview.
type CommandPalette struct {
	*tview.Box
	visible bool
	mode    paletteMode

	// tview-native input: query bar and three add-endpoint form fields.
	queryField *tview.InputField
	nameField  *tview.InputField
	urlField   *tview.InputField
	keyField   *tview.InputField
	activeForm int // 0=name 1=url 2=key

	// item list state
	menuItems []PaletteItem
	items     []PaletteItem
	filtered  []int
	sel       int

	// callbacks wired by App
	onClose         func()
	onBack          func() // step back a level (like Esc); backdrop click
	onAddEndpoint   func(name, baseURL, apiKey string)
	onDelEndpoint   func(name string)
	onSelectModel   func(model string)
	onSelectTheme   func(id string)
	onResumeSession func(id string)
	getEndpoints    func() []paletteEndpointInfo
	getSessions     func() []paletteSessionInfo
	getHotkeyItems  func() []PaletteItem
}

func NewCommandPalette() *CommandPalette {
	cp := &CommandPalette{Box: tview.NewBox()}
	cp.SetBorder(true)
	cp.SetBackgroundColor(Theme.Surface)
	cp.SetBorderColor(Theme.BorderFocus)
	cp.SetTitleColor(Theme.Accent)
	cp.SetTitleAlign(tview.AlignRight)

	mkField := func(label string) *tview.InputField {
		f := tview.NewInputField()
		f.SetLabel(label)
		stylePaletteField(f)
		return f
	}

	cp.queryField = mkField("> ")
	cp.queryField.SetChangedFunc(func(_ string) { cp.refilter() })

	cp.nameField = mkField("name     ❯ ")
	cp.urlField = mkField("base url ❯ ")
	cp.keyField = mkField("api key  ❯ ")

	return cp
}

// stylePaletteField applies theme colors to a palette input field.
func stylePaletteField(f *tview.InputField) {
	f.SetLabelStyle(tcell.StyleDefault.Foreground(Theme.Muted).Background(Theme.Surface))
	f.SetFieldTextColor(Theme.Text)
	f.SetFieldBackgroundColor(Theme.Surface)
	f.SetBackgroundColor(Theme.Surface)
}

// Restyle re-applies theme colors after a theme switch.
func (cp *CommandPalette) Restyle() {
	cp.SetBackgroundColor(Theme.Surface)
	cp.SetBorderColor(Theme.BorderFocus)
	cp.SetTitleColor(Theme.Accent)
	stylePaletteField(cp.queryField)
	stylePaletteField(cp.nameField)
	stylePaletteField(cp.urlField)
	stylePaletteField(cp.keyField)
}

// ── Focus delegation ─────────────────────────────────────────────────────────

// Focus sets hasFocus on the palette itself (so Pages routes events here via
// InputHandler) and additionally sets hasFocus on the active sub-field (so its
// TextArea shows the cursor).  We call sub-field.Focus with a no-op delegate to
// avoid a recursive app.SetFocus that would blur the palette again.
func (cp *CommandPalette) Focus(delegate func(p tview.Primitive)) {
	cp.Box.Focus(delegate) // sets cp.Box.hasFocus = true
	// Clear stale cursor from all sub-fields then light up the active one.
	noop := func(tview.Primitive) {}
	cp.queryField.Blur()
	cp.nameField.Blur()
	cp.urlField.Blur()
	cp.keyField.Blur()
	if cp.mode == paletteModeAddEndpoint {
		cp.activeFormField().Focus(noop)
	} else {
		cp.queryField.Focus(noop)
	}
}

// ── public API ────────────────────────────────────────────────────────────────

func (cp *CommandPalette) IsVisible() bool                    { return cp.visible }
func (cp *CommandPalette) GetMode() paletteMode               { return cp.mode }
func (cp *CommandPalette) QueryField() *tview.InputField      { return cp.queryField }
func (cp *CommandPalette) ActiveFormField() *tview.InputField { return cp.activeFormField() }

func (cp *CommandPalette) Open() {
	cp.visible = true
	cp.mode = paletteModeMenu
	cp.items = cp.menuItems
	cp.sel = 0
	cp.queryField.SetText("")
	cp.refilter()
}

func (cp *CommandPalette) Close() {
	cp.visible = false
	if cp.onClose != nil {
		cp.onClose()
	}
}

// SetBackFunc registers a callback that steps the palette back one level (the
// same behaviour as Esc), invoked when the dimmed backdrop is clicked.
func (cp *CommandPalette) SetBackFunc(fn func()) { cp.onBack = fn }

// SwitchMode is the public entry point; called from app.go closures and
// handleGlobalKey.
func (cp *CommandPalette) SwitchMode(m paletteMode) { cp.switchMode(m) }

func (cp *CommandPalette) SetCallbacks(
	onClose func(),
	onAddEndpoint func(name, baseURL, apiKey string),
	onDelEndpoint func(name string),
	onSelectModel func(model string),
	onSelectTheme func(id string),
	onResumeSession func(id string),
	getEndpoints func() []paletteEndpointInfo,
	getSessions func() []paletteSessionInfo,
	getHotkeyItems func() []PaletteItem,
) {
	cp.onClose = onClose
	cp.onAddEndpoint = onAddEndpoint
	cp.onDelEndpoint = onDelEndpoint
	cp.onSelectModel = onSelectModel
	cp.onSelectTheme = onSelectTheme
	cp.onResumeSession = onResumeSession
	cp.getEndpoints = getEndpoints
	cp.getSessions = getSessions
	cp.getHotkeyItems = getHotkeyItems
}

func (cp *CommandPalette) NavigateUp() {
	if cp.sel > 0 {
		cp.sel--
	}
}

func (cp *CommandPalette) NavigateDown() {
	if cp.sel < len(cp.filtered)-1 {
		cp.sel++
	}
}

func (cp *CommandPalette) NextFormField() {
	if cp.activeForm < 2 {
		cp.activeForm++
	}
}

func (cp *CommandPalette) PrevFormField() {
	if cp.activeForm > 0 {
		cp.activeForm--
	}
}

// Confirm executes the action for the current mode (called from handleGlobalKey).
func (cp *CommandPalette) Confirm() { cp.confirm() }

// InputHandler routes keyboard events to the active sub-field.
// Navigation keys (Enter/Esc/Up/Down/Tab) are consumed by handleGlobalKey
// before this is reached, so everything arriving here is text input.
func (cp *CommandPalette) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return cp.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		if !cp.visible {
			return
		}
		var field *tview.InputField
		if cp.mode == paletteModeAddEndpoint {
			field = cp.activeFormField()
		} else {
			field = cp.queryField
		}
		if h := field.InputHandler(); h != nil {
			h(event, setFocus)
		}
	})
}

// MouseHandler handles item selection (single click) and confirmation (double click),
// and form-field focus switching in add-endpoint mode.
func (cp *CommandPalette) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return cp.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !cp.visible {
			return false, nil
		}
		mx, my := event.Position()
		x, y, w, h := cp.GetRect()

		// A click on the dimmed backdrop outside the box steps back, like Esc.
		if mx < x || mx >= x+w || my < y || my >= y+h {
			if action == tview.MouseLeftClick && cp.onBack != nil {
				cp.onBack()
			}
			return true, nil
		}

		// Wheel scrolls the list by moving the selection (which drives the offset).
		switch action {
		case tview.MouseScrollUp:
			if cp.mode != paletteModeAddEndpoint {
				cp.NavigateUp()
			}
			return true, nil
		case tview.MouseScrollDown:
			if cp.mode != paletteModeAddEndpoint {
				cp.NavigateDown()
			}
			return true, nil
		}

		contentY := y + 3 // after: top border, query/hint row, divider
		if my < contentY || my >= y+h-1 {
			if action == tview.MouseLeftDown || action == tview.MouseLeftClick {
				setFocus(cp)
			}
			return true, nil
		}

		row := my - contentY

		if cp.mode == paletteModeAddEndpoint {
			if row < 3 {
				switch action {
				case tview.MouseLeftDown:
					setFocus(cp)
				case tview.MouseLeftClick:
					cp.activeForm = row
					setFocus(cp)
				}
			}
			return true, nil
		}

		itemsH := h - 4
		fi := cp.itemAtRow(row, itemsH)
		if fi < 0 || fi >= len(cp.filtered) {
			return true, nil
		}
		switch action {
		case tview.MouseLeftDown:
			setFocus(cp)
		case tview.MouseLeftClick:
			cp.sel = fi
			setFocus(cp)
		case tview.MouseLeftDoubleClick:
			cp.sel = fi
			cp.confirm()
		}
		return true, nil
	})
}

// SetModelItems replaces the item list in select-model mode (called after async fetch).
func (cp *CommandPalette) SetModelItems(items []PaletteItem) {
	cp.items = items
	cp.refilter()
}

// ── drawing ───────────────────────────────────────────────────────────────────

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

	// Dim the content behind the overlay (drawn by the page underneath) so the
	// palette stands out. Recolour every cell toward a dark slate in place.
	for row := range sh {
		for col := range sw {
			r, combc, st, _ := screen.GetContent(col, row)
			if r == 0 { // continuation cell of a wide rune; styled via its primary
				continue
			}
			screen.SetContent(col, row, r, combc, dimStyle(st))
		}
	}

	w := paletteW
	if w > sw-4 {
		w = sw - 4
	}

	visRows := cp.contentRows()
	if cp.mode == paletteModeAddEndpoint {
		visRows = 0
	}
	h := 4 + visRows
	if cp.mode == paletteModeAddEndpoint {
		h = 4 + 3
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
	y := (sh - h) / 4

	// Self-assign rect so GetRect() and WrapMouseHandler's InRect check reflect
	// the actual visual bounds (Pages gives us the full-screen rect by default).
	cp.SetRect(x, y, w, h)
	cp.SetTitle(" " + cp.modeTitle() + " ")

	// Fix CJK wide chars that straddle the left edge before Box fills the area.
	if x > 0 {
		for row := range h {
			if _, _, st, cw := screen.GetContent(x-1, y+row); cw == 2 {
				screen.SetContent(x-1, y+row, ' ', nil, st)
			}
		}
	}

	cp.Box.DrawForSubclass(screen, cp)

	bg := Theme.Surface
	borderSt := tcell.StyleDefault.Foreground(Theme.BorderFocus).Background(bg)
	leftT, rightT, horiz := tview.Borders.LeftT, tview.Borders.RightT, tview.Borders.Horizontal
	if cp.HasFocus() {
		leftT = tview.BoxDrawingsHeavyVerticalAndRight
		rightT = tview.BoxDrawingsHeavyVerticalAndLeft
		horiz = tview.BoxDrawingsHeavyHorizontal
	}
	mutedSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
	textSt := tcell.StyleDefault.Foreground(Theme.Text).Background(bg)
	selSt := tcell.StyleDefault.Background(paletteSelBg).Foreground(Theme.Text)
	selMutedSt := tcell.StyleDefault.Background(paletteSelBg).Foreground(Theme.Muted)

	// Query row (y+1).
	if cp.mode == paletteModeAddEndpoint {
		drawText(screen, "fill in fields below, Enter to confirm", x+2, y+1, w-4, mutedSt)
	} else {
		cp.queryField.SetBackgroundColor(bg)
		cp.queryField.SetFieldBackgroundColor(bg)
		cp.queryField.SetRect(x+1, y+1, w-2, 1)
		cp.queryField.Draw(screen)
	}

	// Internal divider (y+2) — overwrite Box's │ with ├┤.
	screen.SetContent(x, y+2, leftT, nil, borderSt)
	for col := 1; col < w-1; col++ {
		screen.SetContent(x+col, y+2, horiz, nil, borderSt)
	}
	screen.SetContent(x+w-1, y+2, rightT, nil, borderSt)

	// Content area (y+3 .. y+h-2).
	itemsH := h - 4
	if cp.mode == paletteModeAddEndpoint {
		cp.drawFormFields(screen, x, y+3, w, bg)
	} else {
		cp.drawItems(screen, x, y+3, w, itemsH, selSt, selMutedSt, mutedSt, textSt)
	}
}

func (cp *CommandPalette) drawFormFields(screen tcell.Screen, x, y, w int, bg tcell.Color) {
	formLabels := []string{"name     ❯ ", "base url ❯ ", "api key  ❯ "}
	fields := []*tview.InputField{cp.nameField, cp.urlField, cp.keyField}
	for i, field := range fields {
		rowY := y + i
		if i == cp.activeForm {
			field.SetLabelColor(Theme.Accent)
			field.SetBackgroundColor(bg)
			field.SetFieldBackgroundColor(bg)
			field.SetRect(x+2, rowY, w-4, 1)
			field.Draw(screen)
		} else {
			labelSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
			col := x + 2
			used := drawText(screen, formLabels[i], col, rowY, w-4, labelSt)
			drawText(screen, field.GetText(), col+used, rowY, x+w-2-col-used, labelSt)
		}
	}
}

func (cp *CommandPalette) drawItems(screen tcell.Screen, x, y, w, h int, selSt, selMutedSt, mutedSt, textSt tcell.Style) {
	inner := x + 2

	offset := cp.itemOffset(h)

	// Reserve the rightmost inner column for a scrollbar when content overflows.
	showBar := cp.contentRows() > h
	textRight := x + w - 2 // exclusive right bound for item text
	hlRight := x + w - 1   // exclusive right bound for the selection highlight fill
	if showBar {
		textRight--
		hlRight--
	}

	rowY := y
	for fi := offset; fi < len(cp.filtered); fi++ {
		ih := cp.itemHeight(fi)
		if rowY+ih > y+h {
			break // no room for the whole entry; stop cleanly
		}
		item := cp.items[cp.filtered[fi]]
		isSel := fi == cp.sel

		lineSt := textSt
		subSt := mutedSt
		if isSel {
			lineSt = selSt
			subSt = selMutedSt
			for r := range ih {
				for col := x + 1; col < hlRight; col++ {
					screen.SetContent(col, rowY+r, ' ', nil, selSt)
				}
			}
		}

		// Primary line: label left-aligned.
		col := inner
		labelFg, _, _ := lineSt.Decompose()
		_, labelW := tview.Print(screen, item.Label, col, rowY, textRight-col, tview.AlignLeft, labelFg)

		// Sub right-aligned when there is room (at least 1-col gap after label).
		if item.Sub != "" {
			subFg, _, _ := subSt.Decompose()
			subW := tview.TaggedStringWidth(item.Sub) // honor color tags in Sub
			subStart := textRight - subW
			if subStart > inner+labelW+1 {
				tview.Print(screen, item.Sub, subStart, rowY, subW, tview.AlignLeft, subFg)
			}
		}

		// Optional dim second line.
		if item.Detail != "" {
			subFg, _, _ := subSt.Decompose()
			tview.Print(screen, item.Detail, inner, rowY+1, textRight-inner, tview.AlignLeft, subFg)
		}

		rowY += ih
	}

	if showBar {
		trackSt := tcell.StyleDefault.Background(Theme.Surface).Foreground(Theme.Muted)
		thumbSt := tcell.StyleDefault.Background(Theme.Border)
		drawScrollbarTrack(screen, x+w-2, y, h, cp.contentRows(), h, cp.rowsBefore(offset), ' ', ' ', trackSt, thumbSt)
	}
}

// itemHeight returns the number of rows the filtered entry at fi occupies.
func (cp *CommandPalette) itemHeight(fi int) int {
	if cp.items[cp.filtered[fi]].Detail != "" {
		return 2
	}
	return 1
}

// contentRows is the total row height of all filtered entries.
func (cp *CommandPalette) contentRows() int {
	n := 0
	for fi := range cp.filtered {
		n += cp.itemHeight(fi)
	}
	return n
}

// rowsBefore is the combined row height of the filtered entries before offset.
func (cp *CommandPalette) rowsBefore(offset int) int {
	n := 0
	for fi := range offset {
		n += cp.itemHeight(fi)
	}
	return n
}

// itemOffset returns the first visible filtered index so that the selected entry
// stays within a window of visH rows, filling upward from the selection.
func (cp *CommandPalette) itemOffset(visH int) int {
	off := cp.sel
	if off >= len(cp.filtered) {
		off = len(cp.filtered) - 1
	}
	if off < 0 {
		return 0
	}
	used := cp.itemHeight(off)
	for off > 0 {
		ph := cp.itemHeight(off - 1)
		if used+ph > visH {
			break
		}
		used += ph
		off--
	}
	return off
}

// itemAtRow maps a content-area row (0-based from the top of the item region) to
// a filtered index, or -1 when the row is past the last visible entry.
func (cp *CommandPalette) itemAtRow(relRow, visH int) int {
	offset := cp.itemOffset(visH)
	rowY := 0
	for fi := offset; fi < len(cp.filtered); fi++ {
		ih := cp.itemHeight(fi)
		if rowY+ih > visH {
			break
		}
		if relRow >= rowY && relRow < rowY+ih {
			return fi
		}
		rowY += ih
	}
	return -1
}

// ── internal helpers ──────────────────────────────────────────────────────────

func (cp *CommandPalette) activeFormField() *tview.InputField {
	switch cp.activeForm {
	case 0:
		return cp.nameField
	case 1:
		return cp.urlField
	default:
		return cp.keyField
	}
}

func (cp *CommandPalette) switchMode(m paletteMode) {
	cp.mode = m
	cp.queryField.SetText("")
	cp.sel = 0

	switch m {
	case paletteModeMenu:
		cp.items = cp.menuItems

	case paletteModeAddEndpoint:
		cp.activeForm = 0
		cp.nameField.SetText("")
		cp.urlField.SetText("")
		cp.keyField.SetText("")
		cp.items = nil

	case paletteModeDelEndpoint:
		eps := cp.getEndpoints()
		cp.items = make([]PaletteItem, len(eps))
		for i, ep := range eps {
			cp.items[i] = PaletteItem{Label: ep.Name, Sub: ep.BaseURL, Value: ep.Name}
		}

	case paletteModeSelectModel:
		cp.items = []PaletteItem{{Label: "fetching models…"}}

	case paletteModeResumeSession:
		sessions := cp.getSessions()
		if len(sessions) == 0 {
			cp.items = []PaletteItem{{Label: "no saved sessions"}}
		} else {
			cp.items = make([]PaletteItem, len(sessions))
			for i, s := range sessions {
				title := s.Title
				if title == "" {
					title = "(untitled)"
				}
				cp.items[i] = PaletteItem{
					Label: title,
					Sub:   formatSessionTime(s.UpdatedAt),
					Value: s.ID,
				}
			}
		}

	case paletteModeHotkeys:
		cp.items = cp.getHotkeyItems()

	case paletteModeSelectTheme:
		choices := ThemeChoices()
		current := CurrentThemeID()
		cp.items = make([]PaletteItem, len(choices))
		for i, c := range choices {
			sub := c.ID
			if c.ID == current {
				sub = "current · " + c.ID
				cp.sel = i // start highlighted on the active theme
			}
			cp.items[i] = PaletteItem{Label: c.Name, Sub: sub, Detail: c.Blocks, Value: c.ID}
		}
	}
	cp.refilter()
}

func (cp *CommandPalette) confirm() {
	switch cp.mode {
	case paletteModeMenu, paletteModeHotkeys:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if item.Action != nil {
			item.Action()
		}

	case paletteModeAddEndpoint:
		name := strings.TrimSpace(cp.nameField.GetText())
		baseURL := strings.TrimSpace(cp.urlField.GetText())
		apiKey := strings.TrimSpace(cp.keyField.GetText())
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

	case paletteModeResumeSession:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if item.Value == "" {
			return // "no saved sessions" placeholder
		}
		if cp.onResumeSession != nil {
			cp.onResumeSession(item.Value)
		}
		cp.Close()

	case paletteModeSelectTheme:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if cp.onSelectTheme != nil {
			cp.onSelectTheme(item.Value)
		}
		cp.Close()
	}
}

func (cp *CommandPalette) refilter() {
	q := strings.ToLower(cp.queryField.GetText())
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

func topLevelItems() []PaletteItem {
	return []PaletteItem{
		{Label: "Resume session", Sub: "continue a previous conversation"},
		{Label: "New session", Sub: "save current and start fresh"},
		{Label: "Compact conversation", Sub: "summarize and compress history"},
		{Label: "Toggle plan mode", Sub: "explore and plan without making changes"},
		{Label: "Add endpoint", Sub: "register a new API endpoint"},
		{Label: "Delete endpoint", Sub: "remove a saved endpoint"},
		{Label: "Select model", Sub: "choose model from an endpoint"},
		{Label: "Switch theme", Sub: "change the color scheme"},
		{Label: "Hotkeys", Sub: "view and trigger keyboard shortcuts"},
	}
}

// dimStyle greys a cell's style for the palette backdrop: backgrounds collapse
// most of the way to dimSlate for a flat wash, foregrounds only halfway so text
// stays faintly legible underneath.
func dimStyle(st tcell.Style) tcell.Style {
	fg, bg, attr := st.Decompose()
	return tcell.StyleDefault.
		Foreground(dimColor(fg, Theme.Text, 0.5)).
		Background(dimColor(bg, Theme.Background, 0.7)).
		Attributes(attr &^ tcell.AttrBold)
}

// dimColor blends c toward dimSlate by t in [0,1], substituting fallback when c
// is the terminal default (whose RGB is not meaningful).
func dimColor(c, fallback tcell.Color, t float64) tcell.Color {
	if c == tcell.ColorDefault {
		c = fallback
	}
	r, g, b := c.RGB()
	tr, tg, tb := dimSlate.RGB()
	lerp := func(a, b int32) int32 { return a + int32(float64(b-a)*t) }
	return tcell.NewRGBColor(lerp(r, tr), lerp(g, tg), lerp(b, tb))
}

func (cp *CommandPalette) modeTitle() string {
	switch cp.mode {
	case paletteModeAddEndpoint:
		return "add endpoint"
	case paletteModeDelEndpoint:
		return "delete endpoint"
	case paletteModeSelectModel:
		return "select model"
	case paletteModeResumeSession:
		return "resume session"
	case paletteModeHotkeys:
		return "hotkeys"
	case paletteModeSelectTheme:
		return "switch theme"
	default:
		return "command palette"
	}
}

func formatSessionTime(ms int64) string {
	t := time.UnixMilli(ms)
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	if t.Year() == now.Year() {
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2 2006")
}
