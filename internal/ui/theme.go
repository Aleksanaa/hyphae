package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	tint "github.com/lrstanley/bubbletint/v2"
	"github.com/rivo/tview"
)

// ActiveTint is the bubbletint tint currently driving every color in the UI,
// including the chroma syntax-highlighting style (see diffsyntax.go). All colors
// below are derived from it by applyTint, so swapping the tint re-themes the whole
// application. Never assign this directly — go through SetThemeByID / applyTint.
var ActiveTint = tint.TintCatppuccinMocha

// toTcell converts a bubbletint color to a tcell RGB color.
func toTcell(c *tint.Color) tcell.Color {
	return tcell.NewRGBColor(int32(c.R), int32(c.G), int32(c.B))
}

// mix linearly blends two tint colors. t=0 returns a, t=1 returns b. Used to
// derive the handful of semantic shades (dimmed/muted/surface variants) that the
// tint's 16-color ANSI palette does not provide a direct slot for.
func mix(a, b *tint.Color, t float64) tcell.Color {
	lerp := func(x, y uint8) int32 {
		return int32(float64(x) + (float64(y)-float64(x))*t)
	}
	return tcell.NewRGBColor(lerp(a.R, b.R), lerp(a.G, b.G), lerp(a.B, b.B))
}

// Theme holds the color palette used across the UI. Every field is (re)populated
// by applyTint from ActiveTint.
var Theme = struct {
	// Background layers
	Background tcell.Color
	Surface    tcell.Color

	// Text
	Text   tcell.Color
	Muted  tcell.Color
	Faint  tcell.Color // Text nudged toward Bg — secondary body copy (e.g. thoughts)
	Accent tcell.Color

	// Cursor is the terminal (hardware) cursor color, applied via
	// screen.SetCursorStyle. Falls back to Text for tints that omit it.
	Cursor tcell.Color

	// Message roles
	UserColor    tcell.Color
	ApexColor    tcell.Color
	ApexDim      tcell.Color // slightly darker ApexColor for status text
	ToolColor    tcell.Color
	ShellColor   tcell.Color
	SystemColor  tcell.Color
	ErrorColor   tcell.Color
	SuccessColor tcell.Color

	// Code — default color for inline code spans and unlanguaged code blocks
	// (text hue shifted slightly blue; syntax-highlighted blocks override per-token)
	CodeColor tcell.Color

	// Borders & chrome
	Border       tcell.Color
	BorderFocus  tcell.Color
	Header       tcell.Color
	PendingColor tcell.Color // amber — awaiting approval

	// Status bar
	StatusBg   tcell.Color
	StatusText tcell.Color
}{}

// TC holds pre-computed tview color tag strings for each Theme color.
var TC struct {
	Background   string
	Surface      string
	Text         string
	Muted        string
	Faint        string
	Accent       string
	UserColor    string
	ApexColor    string
	ApexDim      string
	ToolColor    string
	ShellColor   string
	SystemColor  string
	ErrorColor   string
	SuccessColor string
	CodeColor    string
	Border       string
	BorderFocus  string
	Header       string
	PendingColor string
	StatusBg     string
	StatusText   string
}

// Derived colors used by individual widgets. These are not simple 1:1 tint slots
// (they are blends of Bg with a role hue), so they live here and are recomputed
// by applyTint alongside Theme. Declared without initializers — applyTint owns
// their values.
var (
	// diff view
	diffAddedBg   tcell.Color
	diffRemovedBg tcell.Color
	diffHunkBg    tcell.Color
	diffAddedFg   tcell.Color
	diffRemovedFg tcell.Color
	diffHunkFg    tcell.Color

	// status bar meters
	barEmptyBg   tcell.Color
	barFillBlue  tcell.Color
	barFillAmber tcell.Color
	barFillRed   tcell.Color

	// approval / diff deny field states
	approvalDarkGreen tcell.Color
	approvalDarkRed   tcell.Color
	approvalWhite     tcell.Color

	// selection highlights
	selectHighlightBg tcell.Color // ask_user custom-input highlight
	paletteSelBg      tcell.Color // command-palette highlighted row
	chatSelBg         tcell.Color // chat drag-selection background
	dimSlate          tcell.Color // palette backdrop wash target
)

// applyTint recomputes every derived color from t and makes it the active tint.
// It does not repaint already-constructed widgets — callers switching themes at
// runtime must re-run widget styling (see App.restyle) and redraw.
func applyTint(t *tint.Tint) {
	ActiveTint = t

	// Backgrounds / text. Surface is a subtle lift of Bg toward the tint's dim
	// gray — the tint's SelectionBg is far too light to sit behind panels.
	Theme.Background = toTcell(t.Bg)
	Theme.Surface = mix(t.Bg, t.BrightBlack, 0.18)
	Theme.Text = toTcell(t.Fg)
	Theme.Muted = mix(t.BrightBlack, t.Fg, 0.45)
	Theme.Faint = mix(t.Fg, t.Bg, 0.18)

	// The cursor color is optional in the tint schema; fall back to the fg.
	if t.Cursor != nil {
		Theme.Cursor = toTcell(t.Cursor)
	} else {
		Theme.Cursor = Theme.Text
	}

	// Role hues: user=cyan, apex=purple (the tint's iconic "blue"), accents=pink.
	Theme.Accent = toTcell(t.BrightPurple)
	Theme.UserColor = toTcell(t.Cyan)
	Theme.ApexColor = toTcell(t.Blue)
	Theme.ApexDim = mix(t.Blue, t.Bg, 0.35)
	Theme.ToolColor = toTcell(t.Green)
	Theme.ShellColor = toTcell(t.Yellow)
	Theme.SystemColor = toTcell(t.Blue)
	Theme.ErrorColor = toTcell(t.Red)
	Theme.SuccessColor = toTcell(t.BrightGreen)
	Theme.CodeColor = mix(t.Cyan, t.Fg, 0.5)

	// Chrome.
	Theme.Border = toTcell(t.BrightBlack)
	Theme.BorderFocus = toTcell(t.Blue)
	Theme.Header = toTcell(t.BrightPurple)
	Theme.PendingColor = toTcell(t.Yellow)

	// Status bar.
	Theme.StatusBg = mix(t.Bg, t.BrightBlack, 0.14)
	Theme.StatusText = mix(t.BrightBlack, t.Fg, 0.4)

	// Widget-specific derived colors.
	diffAddedBg = mix(t.Bg, t.Green, 0.18)
	diffRemovedBg = mix(t.Bg, t.Red, 0.18)
	diffHunkBg = mix(t.Bg, t.Blue, 0.18)
	diffAddedFg = toTcell(t.Green)
	diffRemovedFg = toTcell(t.Red)
	diffHunkFg = toTcell(t.Blue)

	barEmptyBg = mix(t.Bg, t.BrightBlack, 0.3)
	barFillBlue = mix(t.Bg, t.Blue, 0.5)
	barFillAmber = mix(t.Bg, t.Yellow, 0.45)
	barFillRed = mix(t.Bg, t.Red, 0.5)

	approvalDarkGreen = mix(t.Bg, t.Green, 0.4)
	approvalDarkRed = mix(t.Bg, t.Red, 0.4)
	approvalWhite = toTcell(t.BrightWhite)

	selectHighlightBg = mix(t.Bg, t.Blue, 0.35)
	paletteSelBg = mix(t.Bg, t.Blue, 0.28)
	chatSelBg = mix(t.Bg, t.Blue, 0.4)
	dimSlate = mix(t.Bg, t.Black, 0.15)

	// tview's default PrimitiveBackgroundColor is tcell.ColorBlack. When tview
	// renders a wide character (emoji, CJK) it explicitly writes a space at
	// column x+1 using the current style. If that style's background differs
	// from our Theme.Background the phantom space is visually distinct.
	// Aligning these makes the phantom space invisible.
	tview.Styles.PrimitiveBackgroundColor = Theme.Background
	tview.Styles.PrimaryTextColor = Theme.Text

	TC.Background = Theme.Background.CSS()
	TC.Surface = Theme.Surface.CSS()
	TC.Text = Theme.Text.CSS()
	TC.Muted = Theme.Muted.CSS()
	TC.Faint = Theme.Faint.CSS()
	TC.Accent = Theme.Accent.CSS()
	TC.UserColor = Theme.UserColor.CSS()
	TC.ApexColor = Theme.ApexColor.CSS()
	TC.ApexDim = Theme.ApexDim.CSS()
	TC.ToolColor = Theme.ToolColor.CSS()
	TC.ShellColor = Theme.ShellColor.CSS()
	TC.SystemColor = Theme.SystemColor.CSS()
	TC.ErrorColor = Theme.ErrorColor.CSS()
	TC.SuccessColor = Theme.SuccessColor.CSS()
	TC.CodeColor = Theme.CodeColor.CSS()
	TC.Border = Theme.Border.CSS()
	TC.BorderFocus = Theme.BorderFocus.CSS()
	TC.Header = Theme.Header.CSS()
	TC.PendingColor = Theme.PendingColor.CSS()
	TC.StatusBg = Theme.StatusBg.CSS()
	TC.StatusText = Theme.StatusText.CSS()

	// Rebuild the chroma style from the same tint so syntax highlighting matches.
	rebuildChromaStyle()
}

// SetThemeByID switches the active tint to the bubbletint tint with the given ID.
// It returns false (leaving the theme unchanged) if no such tint exists. It only
// recomputes the palette; repainting live widgets is the caller's job.
func SetThemeByID(id string) bool {
	t, ok := tint.GetTint(id)
	if !ok {
		return false
	}
	applyTint(t)
	return true
}

// CurrentThemeID returns the ID of the active tint.
func CurrentThemeID() string { return ActiveTint.ID }

// ThemeChoice is one selectable tint, for the theme picker.
type ThemeChoice struct {
	ID     string
	Name   string
	Dark   bool
	Blocks string // tview-tagged neofetch-style swatch of the tint's colors
}

// ThemeChoices lists every available tint, sorted by ID.
func ThemeChoices() []ThemeChoice {
	tints := tint.Tints()
	out := make([]ThemeChoice, len(tints))
	for i, t := range tints {
		out[i] = ThemeChoice{ID: t.ID, Name: t.DisplayName, Dark: t.Dark, Blocks: themeBlocks(t)}
	}
	return out
}

// themeBlocks renders a color swatch for a tint: every chromatic accent — the six
// ANSI hues and their bright variants — each as a spaced square, as a
// tview-tagged string.
func themeBlocks(t *tint.Tint) string {
	cols := []*tint.Color{
		t.Red, t.Yellow, t.Green, t.Cyan, t.Blue, t.Purple,
		t.BrightRed, t.BrightYellow, t.BrightGreen, t.BrightCyan, t.BrightBlue, t.BrightPurple,
	}
	var b strings.Builder
	for _, c := range cols {
		if c == nil {
			continue
		}
		fmt.Fprintf(&b, "[%s]■ ", c.Hex())
	}
	return strings.TrimRight(b.String(), " ") + "[-]"
}

func init() {
	// Populate the app-wide bubbletint registry so GetTint / Tints work.
	tint.NewDefaultRegistry()
	applyTint(ActiveTint)

	tview.Borders.HorizontalFocus = tview.BoxDrawingsHeavyHorizontal
	tview.Borders.VerticalFocus = tview.BoxDrawingsHeavyVertical
	tview.Borders.TopLeftFocus = tview.BoxDrawingsHeavyDownAndRight
	tview.Borders.TopRightFocus = tview.BoxDrawingsHeavyDownAndLeft
	tview.Borders.BottomLeftFocus = tview.BoxDrawingsHeavyUpAndRight
	tview.Borders.BottomRightFocus = tview.BoxDrawingsHeavyUpAndLeft
}
