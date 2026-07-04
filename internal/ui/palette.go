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
)

// PaletteItem is one selectable row in the palette list.
type PaletteItem struct {
	Label  string
	Sub    string // dim secondary text shown right-aligned
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
	onAddEndpoint   func(name, baseURL, apiKey string)
	onDelEndpoint   func(name string)
	onSelectModel   func(model string)
	onResumeSession func(id string)
	getEndpoints    func() []paletteEndpointInfo
	getSessions     func() []paletteSessionInfo
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
		f.SetLabelStyle(tcell.StyleDefault.Foreground(Theme.Muted).Background(Theme.Surface))
		f.SetFieldTextColor(Theme.Text)
		f.SetFieldBackgroundColor(Theme.Surface)
		f.SetBackgroundColor(Theme.Surface)
		return f
	}

	cp.queryField = mkField("> ")
	cp.queryField.SetChangedFunc(func(_ string) { cp.refilter() })

	cp.nameField = mkField("name     ❯ ")
	cp.urlField = mkField("base url ❯ ")
	cp.keyField = mkField("api key  ❯ ")

	return cp
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

// SwitchMode is the public entry point; called from app.go closures and
// handleGlobalKey.
func (cp *CommandPalette) SwitchMode(m paletteMode) { cp.switchMode(m) }

func (cp *CommandPalette) SetCallbacks(
	onClose func(),
	onAddEndpoint func(name, baseURL, apiKey string),
	onDelEndpoint func(name string),
	onSelectModel func(model string),
	onResumeSession func(id string),
	getEndpoints func() []paletteEndpointInfo,
	getSessions func() []paletteSessionInfo,
) {
	cp.onClose = onClose
	cp.onAddEndpoint = onAddEndpoint
	cp.onDelEndpoint = onDelEndpoint
	cp.onSelectModel = onSelectModel
	cp.onResumeSession = onResumeSession
	cp.getEndpoints = getEndpoints
	cp.getSessions = getSessions
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
		_, my := event.Position()
		_, y, _, h := cp.GetRect()

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
		offset := 0
		if cp.sel >= itemsH {
			offset = cp.sel - itemsH + 1
		}
		fi := offset + row
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

	w := paletteW
	if w > sw-4 {
		w = sw - 4
	}

	visItems := len(cp.filtered)
	if cp.mode == paletteModeAddEndpoint {
		visItems = 0
	}
	h := 4 + visItems
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
	selSt := tcell.StyleDefault.Background(tcell.NewRGBColor(40, 44, 70)).Foreground(Theme.Text)
	selMutedSt := tcell.StyleDefault.Background(tcell.NewRGBColor(40, 44, 70)).Foreground(Theme.Muted)

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

	offset := 0
	if cp.sel >= h {
		offset = cp.sel - h + 1
	}

	for row := range h {
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
			subSt = selMutedSt
			for col := 1; col < w-1; col++ {
				screen.SetContent(x+col, rowY, ' ', nil, selSt)
			}
		}

		// Label left-aligned.
		col := inner
		labelFg, _, _ := lineSt.Decompose()
		_, labelW := tview.Print(screen, item.Label, col, rowY, x+w-2-col, tview.AlignLeft, labelFg)
		col += labelW

		// Sub right-aligned when there is room (at least 1-col gap after label).
		if item.Sub != "" {
			subFg, _, _ := subSt.Decompose()
			subW := tview.TaggedStringWidth(tview.Escape(item.Sub))
			subStart := x + w - 2 - subW
			if subStart > inner+labelW+1 {
				tview.Print(screen, item.Sub, subStart, rowY, subW, tview.AlignLeft, subFg)
			}
		}
	}
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
	}
	cp.refilter()
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
	case paletteModeResumeSession:
		return "resume session"
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
