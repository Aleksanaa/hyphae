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
	Text   tcell.Color
	Muted  tcell.Color
	Accent tcell.Color

	// Message roles
	UserColor    tcell.Color
	ApexColor    tcell.Color
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
}{
	Background: tcell.NewRGBColor(16, 16, 24),
	Surface:    tcell.NewRGBColor(24, 24, 36),

	Text:   tcell.NewRGBColor(220, 220, 230),
	Muted:  tcell.NewRGBColor(120, 120, 140),
	Accent: tcell.NewRGBColor(130, 170, 255),

	UserColor:    tcell.NewRGBColor(100, 200, 255),
	ApexColor:    tcell.NewRGBColor(180, 140, 220),
	ToolColor:    tcell.NewRGBColor(130, 200, 130),
	ShellColor:   tcell.NewRGBColor(200, 160, 100),
	CodeColor:    tcell.NewRGBColor(185, 200, 240),
	SystemColor:  tcell.NewRGBColor(160, 140, 200),
	ErrorColor:   tcell.NewRGBColor(220, 80, 80),
	SuccessColor: tcell.NewRGBColor(80, 200, 120),

	Border:       tcell.NewRGBColor(50, 55, 75),
	BorderFocus:  tcell.NewRGBColor(100, 120, 200),
	Header:       tcell.NewRGBColor(130, 170, 255),
	PendingColor: tcell.NewRGBColor(210, 165, 40),

	StatusBg:   tcell.NewRGBColor(24, 24, 40),
	StatusText: tcell.NewRGBColor(140, 145, 175),
}

// TC holds pre-computed tview color tag strings for each Theme color.
var TC struct {
	Background   string
	Surface      string
	Text         string
	Muted        string
	Accent       string
	UserColor    string
	ApexColor    string
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

func init() {
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
	TC.Accent = Theme.Accent.CSS()
	TC.UserColor = Theme.UserColor.CSS()
	TC.ApexColor = Theme.ApexColor.CSS()
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
}
