package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Theme holds the color palette used across the UI.
var Theme = struct {
	// Background layers
	Background tcell.Color
	Surface    tcell.Color

	// Text
	Text    tcell.Color
	Muted   tcell.Color
	Accent  tcell.Color

	// Message roles
	UserColor      tcell.Color
	AssistantColor tcell.Color
	ToolColor      tcell.Color
	ShellColor     tcell.Color
	SystemColor    tcell.Color
	ErrorColor     tcell.Color
	SuccessColor   tcell.Color

	// Borders & chrome
	Border      tcell.Color
	BorderFocus tcell.Color
	Header      tcell.Color

	// Status bar
	StatusBg   tcell.Color
	StatusText tcell.Color
}{
	Background: tcell.NewRGBColor(16, 16, 24),
	Surface:    tcell.NewRGBColor(24, 24, 36),

	Text:    tcell.NewRGBColor(220, 220, 230),
	Muted:   tcell.NewRGBColor(120, 120, 140),
	Accent:  tcell.NewRGBColor(130, 170, 255),

	UserColor:      tcell.NewRGBColor(100, 200, 255),
	AssistantColor: tcell.NewRGBColor(220, 220, 230),
	ToolColor:      tcell.NewRGBColor(130, 200, 130),
	ShellColor:     tcell.NewRGBColor(200, 160, 100),
	SystemColor:    tcell.NewRGBColor(160, 140, 200),
	ErrorColor:     tcell.NewRGBColor(220, 80, 80),
	SuccessColor:   tcell.NewRGBColor(80, 200, 120),

	Border:      tcell.NewRGBColor(50, 55, 75),
	BorderFocus: tcell.NewRGBColor(100, 120, 200),
	Header:      tcell.NewRGBColor(130, 170, 255),

	StatusBg:   tcell.NewRGBColor(24, 24, 40),
	StatusText: tcell.NewRGBColor(140, 145, 175),
}

func init() {
	// tview's default PrimitiveBackgroundColor is tcell.ColorBlack. When tview
	// renders a wide character (emoji, CJK) it explicitly writes a space at
	// column x+1 using the current style. If that style's background differs
	// from our Theme.Background the phantom space is visually distinct.
	// Aligning these makes the phantom space invisible.
	tview.Styles.PrimitiveBackgroundColor = Theme.Background
	tview.Styles.PrimaryTextColor = Theme.Text
}

// tviewColor converts a tcell.Color to a tview-compatible color string.
func tviewColor(c tcell.Color) string {
	r, g, b := c.RGB()
	return colorHex(r, g, b)
}

func colorHex(r, g, b int32) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 7)
	buf[0] = '#'
	buf[1] = hex[r>>4]
	buf[2] = hex[r&0xf]
	buf[3] = hex[g>>4]
	buf[4] = hex[g&0xf]
	buf[5] = hex[b>>4]
	buf[6] = hex[b&0xf]
	return string(buf)
}
