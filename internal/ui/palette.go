package ui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/aleksanaa/hyphae/internal/strutil"
)

// paletteMode controls what the palette is showing.
type paletteMode int

const (
	paletteModeMenu            paletteMode = iota // top-level command list
	paletteModeAddEndpoint                        // form: name + base_url + api_key (add or edit)
	paletteModeManageEndpoints                    // list: add new + existing endpoints
	paletteModeSelectModel                        // list of models to pick
	paletteModeResumeSession                      // list of past sessions to resume
	paletteModeHotkeys                            // list of keyboard shortcuts
	paletteModeSelectTheme                        // list of color themes to pick
	paletteModeSelectSkill                        // list of skills to force-load for the next message
	paletteModeConfirm                            // prompt + choice buttons (a dialog)
)

// PaletteItem is one selectable entry in the palette list. An entry occupies one
// row normally, or two when Detail or DetailRight is set (they render on a dim
// second line, Detail left-aligned and DetailRight right-aligned).
type PaletteItem struct {
	Label       string // primary line, left-aligned (supports color tags)
	Sub         string // dim secondary text shown right-aligned on the primary line
	Detail      string // optional dim second line, left-aligned; empty means single-row unless DetailRight is set
	DetailRight string // optional dim second-line text, right-aligned
	DetailPath  bool   // when set, Detail is a filesystem path shortened to fit its column
	Value       string // opaque payload passed to callbacks
	Action      func() // called on Enter in menu mode
}

type paletteEndpointInfo struct {
	Name    string
	BaseURL string
	APIKey  string
}

type paletteSkillInfo struct {
	Name        string
	Description string
	Loaded      bool // currently loaded on the active session
}

type paletteSessionInfo struct {
	ID            string
	Title         string
	UpdatedAt     int64
	WorkDir       string
	ContextWindow int64
	PromptTokens  int64
}

// CommandPalette is a VS-Code-style Ctrl+P overlay drawn as a centered box.
// Text input is handled by embedded tview.InputField instances so that cursor
// rendering, CJK input, and wide-char layout are all native to tview.
type CommandPalette struct {
	*tview.Box
	visible bool
	mode    paletteMode

	// tview-native input: query bar and the three add-endpoint fields. keyField
	// holds the real api key for editing/paste but is never drawn directly — the
	// api-key row is rendered masked (see drawFormKey).
	queryField   *tview.InputField
	nameField    *tview.InputField
	urlField     *tview.InputField
	keyField     *tview.InputField
	activeForm   int    // 0=name 1=url 2=key 3=delete button (edit mode only)
	editEndpoint string // original name when editing an existing endpoint; "" when adding new
	keyExisting  string // the endpoint's stored api key when editing; shown masked as a grey hint

	// item list state
	menuItems []PaletteItem
	items     []PaletteItem
	filtered  []int
	sel       int
	top       int // first visible filtered index (persistent scroll position)

	// loadedSkills marks which skills were force-loaded during the current
	// select-skill session, so their row shows a green "loaded" and re-selecting
	// them does not queue a duplicate reminder. Reset each time the mode is entered.
	loadedSkills map[string]bool

	// dialog state (paletteModeConfirm): a prompt above the choice items, plus the
	// step-back action (Esc / backdrop click) for the current dialog.
	dialogTitle string
	dialogLines []string
	dialogBack  func()

	// scrollbar is the shared Scrollbar primitive, driven in content-row units.
	// The palette owns it directly (like its input fields) rather than through a
	// tview layout, driving its Draw and forwarding mouse events to it.
	scrollbar *Scrollbar
	trackH    int // scrollbar/list viewport height in rows, set each Draw

	// callbacks wired by App
	onClose          func()
	onBack           func() // step back a level (like Esc); backdrop click
	onAddEndpoint    func(origName, name, baseURL, apiKey string)
	onDeleteEndpoint func(name, url string) // raises the delete-confirm dialog
	onSelectModel    func(model string)
	onSelectTheme    func(id string)
	onSelectSkill    func(name string) // force-load a skill for the next message
	onUnloadSkill    func(name string) // unload a previously-loaded skill
	onResumeSession  func(id, workDir string)
	getEndpoints     func() []paletteEndpointInfo
	getSessions      func() []paletteSessionInfo
	getSkills        func() []paletteSkillInfo
	getHotkeyItems   func() []PaletteItem
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

	// The list scrolls in content-row units (entries may be 1 or 2 rows tall);
	// scrollTo receives a row offset which we map back to a filtered index.
	cp.scrollbar = NewScrollbar(
		cp.contentRows,
		func() int { return cp.trackH },
		func() int { return cp.rowsBefore(cp.itemOffset(cp.trackH)) },
		cp.scrollToRow,
	)

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
// InputHandler) and additionally sets hasFocus on the active sub-field (so it
// shows the cursor).  We call sub-field.Focus with a no-op delegate to avoid a
// recursive app.SetFocus that would blur the palette again.
func (cp *CommandPalette) Focus(delegate func(p tview.Primitive)) {
	cp.Box.Focus(delegate) // sets cp.Box.hasFocus = true
	// Clear stale cursor from all sub-fields then light up the active one.
	noop := func(tview.Primitive) {}
	cp.queryField.Blur()
	cp.nameField.Blur()
	cp.urlField.Blur()
	cp.keyField.Blur()
	if cp.mode == paletteModeAddEndpoint {
		// The Delete button (index 3) is not a field, so leave the cursor off.
		if f := cp.activeFormPrimitive(); f != nil {
			f.Focus(noop)
		}
	} else {
		cp.queryField.Focus(noop)
	}
}

// ── public API ────────────────────────────────────────────────────────────────

func (cp *CommandPalette) IsVisible() bool               { return cp.visible }
func (cp *CommandPalette) GetMode() paletteMode          { return cp.mode }
func (cp *CommandPalette) QueryField() *tview.InputField { return cp.queryField }

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

// ShowDialog switches the palette into a confirm dialog: a prompt (one entry per
// line) above a list of choices. Each choice is a PaletteItem whose Action runs
// when it is confirmed (Enter or double-click). onBack is the step-back action
// (Esc / backdrop click). The palette must already be visible (dialogs are raised
// mid-flow, e.g. from a list selection).
func (cp *CommandPalette) ShowDialog(title string, lines []string, choices []PaletteItem, onBack func()) {
	cp.mode = paletteModeConfirm
	cp.dialogTitle = title
	cp.dialogLines = lines
	cp.dialogBack = onBack
	cp.items = choices
	cp.sel = 0
	cp.top = 0
	cp.queryField.SetText("")
	cp.refilter()
}

// DialogBack runs the current dialog's step-back action (Esc / backdrop click).
func (cp *CommandPalette) DialogBack() {
	if cp.dialogBack != nil {
		cp.dialogBack()
	}
}

// ReopenEndpointForm returns to the endpoint edit form without clearing its
// fields — used to step back from the delete-confirm dialog raised over it.
func (cp *CommandPalette) ReopenEndpointForm() {
	cp.mode = paletteModeAddEndpoint
	cp.queryField.SetText("")
	cp.refilter()
}

// EditEndpoint opens the endpoint form pre-filled with an existing endpoint's
// values, in edit mode (a Delete button appears and saving updates in place).
func (cp *CommandPalette) EditEndpoint(name, baseURL, apiKey string) {
	cp.mode = paletteModeAddEndpoint
	cp.editEndpoint = name
	cp.activeForm = 0
	cp.nameField.SetText(name)
	cp.urlField.SetText(baseURL)
	// The api key is not pre-filled as editable text: it shows masked as a grey
	// hint (see drawFormKey), and is kept on save unless the user types a new one.
	cp.keyExisting = apiKey
	cp.keyField.SetText("")
	cp.items = nil
	cp.sel = 0
	cp.queryField.SetText("")
	cp.refilter()
}

// maxFormIndex is the highest focusable form index: 3 (with Delete button) when
// editing, 2 (three fields only) when adding a new endpoint.
func (cp *CommandPalette) maxFormIndex() int {
	if cp.editEndpoint != "" {
		return 3
	}
	return 2
}

// formRows is the number of content rows the endpoint form occupies: the name,
// base url, and api key rows, plus a blank spacer and a Delete button when editing.
func (cp *CommandPalette) formRows() int {
	if cp.editEndpoint != "" {
		return 5
	}
	return 3
}

func (cp *CommandPalette) SetCallbacks(
	onClose func(),
	onAddEndpoint func(origName, name, baseURL, apiKey string),
	onDeleteEndpoint func(name, url string),
	onSelectModel func(model string),
	onSelectTheme func(id string),
	onSelectSkill func(name string),
	onUnloadSkill func(name string),
	onResumeSession func(id, workDir string),
	getEndpoints func() []paletteEndpointInfo,
	getSessions func() []paletteSessionInfo,
	getSkills func() []paletteSkillInfo,
	getHotkeyItems func() []PaletteItem,
) {
	cp.onClose = onClose
	cp.onAddEndpoint = onAddEndpoint
	cp.onDeleteEndpoint = onDeleteEndpoint
	cp.onSelectModel = onSelectModel
	cp.onSelectTheme = onSelectTheme
	cp.onSelectSkill = onSelectSkill
	cp.onUnloadSkill = onUnloadSkill
	cp.onResumeSession = onResumeSession
	cp.getEndpoints = getEndpoints
	cp.getSessions = getSessions
	cp.getSkills = getSkills
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
	if cp.activeForm < cp.maxFormIndex() {
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
		if cp.mode == paletteModeConfirm {
			return // a dialog has no text field; navigation is handled globally
		}
		var field tview.Primitive = cp.queryField
		if cp.mode == paletteModeAddEndpoint {
			field = cp.activeFormPrimitive()
			if field == nil {
				return // Delete button has no text field
			}
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
		itemsH := h - 4

		// A press on the scrollbar column is forwarded to the shared Scrollbar
		// primitive, which owns click/drag handling. Returning its capture value
		// lets tview route the rest of the drag straight to it (see WrapMouseHandler).
		showBar := cp.mode != paletteModeAddEndpoint && cp.contentRows() > itemsH
		if showBar && action == tview.MouseLeftDown && mx == x+w-2 && my >= contentY && my < contentY+itemsH {
			return cp.scrollbar.MouseHandler()(action, event, setFocus)
		}

		if my < contentY || my >= y+h-1 {
			if action == tview.MouseLeftDown || action == tview.MouseLeftClick {
				setFocus(cp)
			}
			return true, nil
		}

		row := my - contentY

		// In a dialog the choices sit below the prompt; map the click accordingly.
		if pr := cp.dialogPromptRows(); pr > 0 {
			row -= pr
			if row < 0 {
				return true, nil
			}
		}

		if cp.mode == paletteModeAddEndpoint {
			// Rows: 0=name 1=url 2=key 3=blank 4=delete.
			idx := -1
			switch {
			case row < 3:
				idx = row
			case cp.editEndpoint != "" && row == 4:
				idx = 3
			}
			if idx < 0 {
				return true, nil
			}
			switch action {
			case tview.MouseLeftDown:
				setFocus(cp)
			case tview.MouseLeftClick:
				cp.activeForm = idx
				setFocus(cp)
			case tview.MouseLeftDoubleClick:
				if idx == 3 { // Delete button
					cp.activeForm = 3
					cp.confirm()
				}
			}
			return true, nil
		}

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
			// A confirm may switch modes (e.g. list → form) without closing;
			// re-focus the palette so the new mode's field/cursor lights up.
			if cp.visible {
				setFocus(cp)
			}
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
			r, combc, st, cw := screen.GetContent(col, row)
			if r == 0 { // continuation cell of a wide rune; styled via its primary
				continue
			}
			if emojiGlyph(r) {
				// Color-emoji glyphs are painted by the terminal with their own
				// baked-in colors and ignore the SGR styling we set, so they
				// refuse to dim with the rest of the backdrop. Paint over them
				// with dimmed blanks instead of just restyling.
				for i := 0; i < cw && col+i < sw; i++ {
					screen.SetContent(col+i, row, ' ', nil, dimStyle(st))
				}
				continue
			}
			screen.SetContent(col, row, r, combc, dimStyle(st))
		}
	}

	w := paletteW
	if w > sw-4 {
		w = sw - 4
	}

	visRows := cp.contentRows() + cp.dialogPromptRows()
	if cp.mode == paletteModeAddEndpoint {
		visRows = 0
	}
	h := 4 + visRows
	if cp.mode == paletteModeAddEndpoint {
		h = 4 + cp.formRows()
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
	switch cp.mode {
	case paletteModeAddEndpoint:
		drawText(screen, "fill in fields below, Enter to confirm", x+2, y+1, w-4, mutedSt)
	case paletteModeConfirm:
		drawText(screen, "↑↓ choose · Enter confirm · Esc cancel", x+2, y+1, w-4, mutedSt)
	default:
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
	cp.trackH = itemsH
	switch cp.mode {
	case paletteModeAddEndpoint:
		cp.drawFormFields(screen, x, y+3, w, bg)
	case paletteModeConfirm:
		pr := cp.dialogPromptRows()
		cp.drawDialogPrompt(screen, x, y+3, w, textSt)
		cp.drawItems(screen, x, y+3+pr, w, itemsH-pr, selSt, selMutedSt, mutedSt, textSt)
	default:
		cp.drawItems(screen, x, y+3, w, itemsH, selSt, selMutedSt, mutedSt, textSt)
	}
}

// dialogPromptRows is the number of content rows the dialog prompt occupies
// (one per line plus a trailing blank), or 0 outside dialog mode.
func (cp *CommandPalette) dialogPromptRows() int {
	if cp.mode != paletteModeConfirm || len(cp.dialogLines) == 0 {
		return 0
	}
	return len(cp.dialogLines) + 1
}

// drawDialogPrompt renders the dialog's prompt lines above its choices.
func (cp *CommandPalette) drawDialogPrompt(screen tcell.Screen, x, y, w int, st tcell.Style) {
	fg, _, _ := st.Decompose()
	for i, line := range cp.dialogLines {
		tview.Print(screen, line, x+2, y+i, w-4, tview.AlignLeft, fg)
	}
}

func (cp *CommandPalette) drawFormFields(screen tcell.Screen, x, y, w int, bg tcell.Color) {
	// name (row 0) and base url (row 1) are plain single-line inputs.
	cp.drawFormInput(screen, cp.nameField, "name     ❯ ", x, y, w, bg, cp.activeForm == 0)
	cp.drawFormInput(screen, cp.urlField, "base url ❯ ", x, y+1, w, bg, cp.activeForm == 1)

	// api key (row 2) is masked (never draws keyField directly).
	cp.drawFormKey(screen, x, y+2, w, bg, cp.activeForm == 2)

	// Delete button (edit mode only) at row 4, after a blank spacer row.
	if cp.editEndpoint != "" {
		rowY := y + 4
		if cp.activeForm == 3 {
			selSt := tcell.StyleDefault.Background(paletteSelBg).Foreground(Theme.Text)
			for col := x + 1; col < x+w-1; col++ {
				screen.SetContent(col, rowY, ' ', nil, selSt)
			}
		}
		label := fmt.Sprintf("[%s]Delete endpoint[-]", TC.ErrorColor)
		tview.Print(screen, label, x+2, rowY, w-4, tview.AlignLeft, Theme.Text)
	}
}

// drawFormInput draws a single-line form input: the focused field renders
// natively (with its cursor); the rest are drawn dimmed with a manual label.
func (cp *CommandPalette) drawFormInput(screen tcell.Screen, f *tview.InputField, label string, x, rowY, w int, bg tcell.Color, active bool) {
	if active {
		f.SetLabelColor(Theme.Accent)
		f.SetBackgroundColor(bg)
		f.SetFieldBackgroundColor(bg)
		f.SetRect(x+2, rowY, w-4, 1)
		f.Draw(screen)
		return
	}
	labelSt := tcell.StyleDefault.Foreground(Theme.Muted).Background(bg)
	col := x + 2
	used := drawText(screen, label, col, rowY, w-4, labelSt)
	drawText(screen, f.GetText(), col+used, rowY, x+w-2-col-used, labelSt)
}

// drawFormKey renders the masked api-key row. keyField holds the real value; the
// display is always masked (see maskAPIKey). When the field is empty during an
// edit, the stored key is shown masked as a grey hint; typing replaces it.
func (cp *CommandPalette) drawFormKey(screen tcell.Screen, x, rowY, w int, bg tcell.Color, active bool) {
	labelColor := Theme.Muted
	if active {
		labelColor = Theme.Accent
	}
	labelSt := tcell.StyleDefault.Foreground(labelColor).Background(bg)
	col := x + 2
	used := drawText(screen, "api key  ❯ ", col, rowY, w-4, labelSt)
	valX := col + used
	valW := x + w - 2 - valX
	if valW < 1 {
		return
	}

	real := cp.keyField.GetText()
	if real == "" {
		// Empty: show the stored key masked as a grey hint (edit mode only).
		if cp.keyExisting != "" {
			hint := truncMiddle(maskAPIKey(cp.keyExisting), valW)
			drawText(screen, hint, valX, rowY, valW, tcell.StyleDefault.Foreground(Theme.Muted).Background(bg))
		}
		if active {
			screen.ShowCursor(valX, rowY)
		}
		return
	}
	masked := truncMiddle(maskAPIKey(real), valW)
	vused := drawText(screen, masked, valX, rowY, valW, tcell.StyleDefault.Foreground(Theme.Text).Background(bg))
	if active {
		screen.ShowCursor(min(valX+vused, x+w-2), rowY)
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

		// Optional dim second line: Detail left-aligned, DetailRight right-aligned.
		if item.Detail != "" || item.DetailRight != "" {
			subFg, _, _ := subSt.Decompose()

			leftEnd := textRight // exclusive right bound for the left text
			if item.DetailRight != "" {
				rightW := tview.TaggedStringWidth(item.DetailRight)
				rightStart := textRight - rightW
				tview.Print(screen, item.DetailRight, rightStart, rowY+1, rightW, tview.AlignLeft, subFg)
				leftEnd = rightStart - 1 // keep a 1-col gap before the right text
			}

			left := item.Detail
			availW := leftEnd - inner
			if availW > 0 {
				if item.DetailPath {
					left = shortenPath(left, availW)
				} else {
					// Single-line row with no wrapping: bound the text so an
					// overlong Detail (e.g. a skill description) ends in "…"
					// rather than being hard-clipped. Assumes plain text (no
					// color tags) when long, which holds for our Detail users.
					left = strutil.Truncate(left, availW)
				}
				tview.Print(screen, left, inner, rowY+1, availW, tview.AlignLeft, subFg)
			}
		}

		rowY += ih
	}

	if showBar {
		cp.scrollbar.SetRect(x+w-2, y, 1, h)
		cp.scrollbar.Draw(screen)
	}
}

// itemHeight returns the number of rows the filtered entry at fi occupies.
func (cp *CommandPalette) itemHeight(fi int) int {
	it := cp.items[cp.filtered[fi]]
	if it.Detail != "" || it.DetailRight != "" {
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

// itemOffset reconciles the persistent scroll position (cp.top) with the current
// selection and returns the first visible filtered index. The list only scrolls
// when the selection would fall outside the visH-row window, so the selected row
// can sit anywhere within the window instead of being pinned to an edge.
func (cp *CommandPalette) itemOffset(visH int) int {
	if len(cp.filtered) == 0 {
		cp.top = 0
		return 0
	}
	// Clamp into range: filtering may have shrunk the list, and we never want to
	// leave blank rows below when there is content that could fill them.
	if mt := cp.maxTop(visH); cp.top > mt {
		cp.top = mt
	}
	if cp.top < 0 {
		cp.top = 0
	}
	// Scroll up only if the selection is above the window's top.
	if cp.sel < cp.top {
		cp.top = cp.sel
	}
	// Scroll down only if the selection is below the window's bottom.
	for cp.lastVisible(cp.top, visH) < cp.sel {
		cp.top++
	}
	return cp.top
}

// scrollToRow is the Scrollbar's scrollTo callback: it takes a target content-row
// offset, maps it to a filtered index, and applies it. Because the palette keeps
// the selection inside the visible window, the selection is nudged to stay within
// the new window so itemOffset does not immediately snap the scroll back.
func (cp *CommandPalette) scrollToRow(rowOff int) {
	cp.top = cp.topForRow(rowOff, cp.trackH)
	if cp.sel < cp.top {
		cp.sel = cp.top
	}
	if last := cp.lastVisible(cp.top, cp.trackH); cp.sel > last {
		cp.sel = last
	}
}

// topForRow returns the filtered index whose top edge is nearest targetRow (in
// content rows from the top), clamped so it never scrolls past maxTop.
func (cp *CommandPalette) topForRow(targetRow, visH int) int {
	best, bestDiff, row := 0, 1<<30, 0
	for fi := range cp.filtered {
		if d := abs(targetRow - row); d < bestDiff {
			best, bestDiff = fi, d
		}
		row += cp.itemHeight(fi)
	}
	if mt := cp.maxTop(visH); best > mt {
		best = mt
	}
	return best
}

// lastVisible returns the last filtered index that fully fits in a visH-row
// window starting at top.
func (cp *CommandPalette) lastVisible(top, visH int) int {
	used := 0
	last := top
	for fi := top; fi < len(cp.filtered); fi++ {
		ih := cp.itemHeight(fi)
		if used+ih > visH {
			break
		}
		used += ih
		last = fi
	}
	return last
}

// maxTop returns the largest scroll offset that still fills the visH-row window
// (i.e. keeps the last entry pinned to the bottom), so scrolling never reveals
// blank space past the end of the list.
func (cp *CommandPalette) maxTop(visH int) int {
	used := 0
	top := len(cp.filtered)
	for fi := len(cp.filtered) - 1; fi >= 0; fi-- {
		ih := cp.itemHeight(fi)
		if used+ih > visH {
			break
		}
		used += ih
		top = fi
	}
	return top
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

// activeFormPrimitive returns the focused form field (name input or url/key text
// area), or nil when the Delete button (index 3) is focused.
func (cp *CommandPalette) activeFormPrimitive() tview.Primitive {
	switch cp.activeForm {
	case 0:
		return cp.nameField
	case 1:
		return cp.urlField
	case 2:
		return cp.keyField
	default:
		return nil
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
		cp.editEndpoint = ""
		cp.keyExisting = ""
		cp.activeForm = 0
		cp.nameField.SetText("")
		cp.urlField.SetText("")
		cp.keyField.SetText("")
		cp.items = nil

	case paletteModeManageEndpoints:
		eps := cp.getEndpoints()
		cp.items = make([]PaletteItem, 0, len(eps)+1)
		cp.items = append(cp.items, PaletteItem{Label: fmt.Sprintf("[%s]+ Add new endpoint[-]", TC.SystemColor), Sub: "register a new API endpoint"})
		for _, ep := range eps {
			cp.items = append(cp.items, PaletteItem{Label: ep.Name, Sub: ep.BaseURL, Value: ep.Name})
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
					Label:       title,
					Sub:         fmt.Sprintf("[%s]%s[-]", TC.Accent, formatSessionTime(s.UpdatedAt)),
					Detail:      s.WorkDir,
					DetailPath:  true,
					DetailRight: formatContextUsage(s.ContextWindow, s.PromptTokens),
					Value:       s.ID + "\x00" + s.WorkDir,
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

	case paletteModeSelectSkill:
		skills := cp.getSkills()
		cp.loadedSkills = make(map[string]bool)
		if len(skills) == 0 {
			cp.items = []PaletteItem{{Label: fmt.Sprintf("[%s]no skills found[-]", TC.Muted)}}
		} else {
			cp.items = make([]PaletteItem, len(skills))
			for i, s := range skills {
				// Descriptions may be multi-line (YAML block scalars); flatten to one line.
				desc := strings.Join(strings.Fields(s.Description), " ")
				it := PaletteItem{Label: s.Name, Detail: desc, Value: s.Name}
				if s.Loaded {
					cp.loadedSkills[s.Name] = true
					it.Sub = fmt.Sprintf("[%s]loaded[-]", TC.SuccessColor)
				}
				cp.items[i] = it
			}
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
		if cp.editEndpoint != "" && cp.activeForm == 3 { // Delete button
			// Confirm before deleting; the callback raises the dialog.
			if cp.onDeleteEndpoint != nil {
				cp.onDeleteEndpoint(cp.editEndpoint, cp.urlField.GetText())
			}
			return
		}
		name := strings.TrimSpace(cp.nameField.GetText())
		baseURL := strings.TrimSpace(cp.urlField.GetText())
		apiKey := strings.TrimSpace(cp.keyField.GetText())
		if apiKey == "" && cp.editEndpoint != "" {
			apiKey = cp.keyExisting // left untouched: keep the stored key
		}
		if name == "" || baseURL == "" || apiKey == "" {
			return
		}
		if cp.onAddEndpoint != nil {
			cp.onAddEndpoint(cp.editEndpoint, name, baseURL, apiKey)
		}
		cp.Close()

	case paletteModeManageEndpoints:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if item.Value == "" { // "+ Add new endpoint"
			cp.switchMode(paletteModeAddEndpoint)
			return
		}
		// Open the pre-filled form for the chosen endpoint.
		for _, ep := range cp.getEndpoints() {
			if ep.Name == item.Value {
				cp.EditEndpoint(ep.Name, ep.BaseURL, ep.APIKey)
				break
			}
		}

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
		// Value is "id\x00workDir"; the callback decides whether to resume
		// directly or raise a dialog, so we don't Close() here.
		id, workDir, _ := strings.Cut(item.Value, "\x00")
		if cp.onResumeSession != nil {
			cp.onResumeSession(id, workDir)
		}

	case paletteModeSelectTheme:
		if len(cp.filtered) == 0 {
			return
		}
		item := cp.items[cp.filtered[cp.sel]]
		if cp.onSelectTheme != nil {
			cp.onSelectTheme(item.Value)
		}
		cp.Close()

	case paletteModeSelectSkill:
		if len(cp.filtered) == 0 {
			return
		}
		idx := cp.filtered[cp.sel]
		item := cp.items[idx]
		if item.Value == "" {
			return // "no skills found" placeholder
		}
		// Toggle: a loaded skill unloads, an unloaded one loads. Stay open so
		// several can be toggled in one pass (Esc to close).
		if cp.loadedSkills[item.Value] {
			if cp.onUnloadSkill != nil {
				cp.onUnloadSkill(item.Value)
			}
			delete(cp.loadedSkills, item.Value)
			cp.items[idx].Sub = ""
		} else {
			if cp.onSelectSkill != nil {
				cp.onSelectSkill(item.Value)
			}
			cp.loadedSkills[item.Value] = true
			cp.items[idx].Sub = fmt.Sprintf("[%s]loaded[-]", TC.SuccessColor)
		}

	case paletteModeConfirm:
		if len(cp.filtered) == 0 {
			return
		}
		if item := cp.items[cp.filtered[cp.sel]]; item.Action != nil {
			item.Action()
		}
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
	cp.top = 0 // reset scroll to the top whenever the visible set changes
}

func topLevelItems() []PaletteItem {
	return []PaletteItem{
		{Label: "Resume session", Sub: "continue a previous conversation"},
		{Label: "New session", Sub: "save current and start fresh"},
		{Label: "Compact conversation", Sub: "summarize and compress history"},
		{Label: "Toggle plan mode", Sub: "explore and plan without making changes"},
		{Label: "Manage endpoints", Sub: "add, edit, or remove API endpoints"},
		{Label: "Select model", Sub: "choose model from an endpoint"},
		{Label: "Load skills", Sub: "load a skill for the next message"},
		{Label: "Switch theme", Sub: "change the color scheme"},
		{Label: "Hotkeys", Sub: "view and trigger keyboard shortcuts"},
	}
}

// emojiGlyph reports whether r is a color-emoji code point. Terminals render
// these with their own baked-in colors, ignoring the foreground/background style
// we set, so they will not dim with the rest of the palette backdrop and must be
// painted over with blanks instead. The set is the supplementary emoji planes
// (r >= 0x1F000, which also covers regional indicators and skin-tone modifiers)
// plus the BMP code points whose Unicode Emoji_Presentation is Yes — the ones a
// terminal shows in color even without a variation selector.
func emojiGlyph(r rune) bool {
	if r >= 0x1F000 {
		return true
	}
	switch {
	case r >= 0x231A && r <= 0x231B, // ⌚⌛
		r >= 0x23E9 && r <= 0x23EC, r == 0x23F0, r == 0x23F3,
		r >= 0x25FD && r <= 0x25FE,
		r >= 0x2614 && r <= 0x2615,
		r >= 0x2648 && r <= 0x2653,
		r == 0x267F, r == 0x2693, r == 0x26A1,
		r >= 0x26AA && r <= 0x26AB,
		r >= 0x26BD && r <= 0x26BE,
		r >= 0x26C4 && r <= 0x26C5,
		r == 0x26CE, r == 0x26D4, r == 0x26EA,
		r >= 0x26F2 && r <= 0x26F3, r == 0x26F5, r == 0x26FA, r == 0x26FD,
		r == 0x2705,
		r >= 0x270A && r <= 0x270B,
		r == 0x2728, r == 0x274C, r == 0x274E,
		r >= 0x2753 && r <= 0x2755, r == 0x2757,
		r >= 0x2795 && r <= 0x2797,
		r == 0x27B0, r == 0x27BF,
		r >= 0x2B1B && r <= 0x2B1C,
		r == 0x2B50, r == 0x2B55:
		return true
	}
	return false
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
		if cp.editEndpoint != "" {
			return "edit endpoint"
		}
		return "add endpoint"
	case paletteModeManageEndpoints:
		return "manage endpoints"
	case paletteModeSelectModel:
		return "select model"
	case paletteModeResumeSession:
		return "resume session"
	case paletteModeHotkeys:
		return "hotkeys"
	case paletteModeSelectTheme:
		return "switch theme"
	case paletteModeSelectSkill:
		return "load skills"
	case paletteModeConfirm:
		return cp.dialogTitle
	default:
		return "command palette"
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
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

// formatContextUsage summarizes how full a session's context is: a percentage of
// the context window when it is known, otherwise the raw prompt-token count.
// Empty when the session has recorded no prompt tokens yet.
func formatContextUsage(window, promptTokens int64) string {
	if promptTokens <= 0 {
		return ""
	}
	if window > 0 {
		pct := float64(promptTokens) / float64(window) * 100
		if pct > 100 {
			pct = 100
		}
		return fmt.Sprintf("%.1f%% context", pct)
	}
	return humanTokens(promptTokens) + " token"
}

// maskAPIKey obscures an api key for display: everything up to and including the
// last '-' is kept, and the remainder has all but its final 4 characters replaced
// with '*' (e.g. "sk-a-b-c-XXXXXXdddd" → "sk-a-b-c-******dddd"). Keys with no '-'
// are masked as a whole; a tail of 4 or fewer characters is left as-is.
func maskAPIKey(key string) string {
	if key == "" {
		return ""
	}
	prefix, suffix := "", key
	if i := strings.LastIndexByte(key, '-'); i >= 0 {
		prefix, suffix = key[:i+1], key[i+1:]
	}
	r := []rune(suffix)
	if len(r) <= 4 {
		return prefix + suffix
	}
	return prefix + strings.Repeat("*", len(r)-4) + string(r[len(r)-4:])
}

// truncMiddle shortens s to at most maxW columns by collapsing its middle into a
// single ellipsis, keeping the head and tail (so a masked key keeps both its
// prefix and its last visible characters). Rune-based; fine for ASCII keys.
func truncMiddle(s string, maxW int) string {
	if tview.TaggedStringWidth(s) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	r := []rune(s)
	keep := maxW - 1
	head := keep / 2
	tail := keep - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// shortenPath renders a directory path to fit maxW display columns. It uses the
// home-abbreviated absolute path ("~/…"), falling back to absolute. When that is
// still too wide it collapses interior segments to their first letter starting
// from the second segment (e.g. ~/w/p/hyphae), and finally hard-truncates with a
// leading ellipsis.
func shortenPath(dir string, maxW int) string {
	if dir == "" || maxW <= 0 {
		return ""
	}
	disp := hpath(dir)
	if tview.TaggedStringWidth(disp) <= maxW {
		return disp
	}
	disp = collapseFromSecond(disp)
	if tview.TaggedStringWidth(disp) <= maxW {
		return disp
	}
	return truncPathLeft(disp, maxW)
}

// hpath abbreviates a leading home directory in p to "~".
func hpath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rel
	}
	return p
}

// collapseFromSecond abbreviates each interior segment of a slash path to its
// first character, starting from the second segment and keeping the first and
// final segments intact. e.g. ~/works/proj/hyphae → ~/w/p/hyphae.
func collapseFromSecond(p string) string {
	sep := string(os.PathSeparator)
	segs := strings.Split(p, sep)
	if len(segs) <= 2 {
		return p
	}
	last := len(segs) - 1
	for i := 1; i < last; i++ {
		if segs[i] == "" {
			continue
		}
		segs[i] = string([]rune(segs[i])[0])
	}
	return strings.Join(segs, sep)
}

// truncPathLeft keeps the tail of s within maxW columns, prefixing an ellipsis
// when characters are dropped.
func truncPathLeft(s string, maxW int) string {
	if tview.TaggedStringWidth(s) <= maxW {
		return s
	}
	if maxW <= 1 {
		return "…"
	}
	r := []rune(s)
	budget := maxW - 1 // reserve one column for the ellipsis
	w, i := 0, len(r)
	for i > 0 {
		cw := tview.TaggedStringWidth(string(r[i-1]))
		if w+cw > budget {
			break
		}
		w += cw
		i--
	}
	return "…" + string(r[i:])
}
