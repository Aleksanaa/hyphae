package ui

import (
	"fmt"
	"math"
	"math/rand"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// The welcome screen shows a mycelial network — threading, dichotomously
// branching hyphae with square growing tips (▪) radiating from a central
// clearing that cradles the "hyphae" wordmark. The network is regenerated once
// per launch (buildHyphaeArt with a time seed) so it looks different every time,
// while the wordmark stays centred in the canvas.

const (
	hyphaeCanvasW = 55
	hyphaeCanvasH = 13
)

var hyphaeWordmark = []string{
	"╻ ╻╻ ╻┏━┓╻ ╻┏━┓┏━╸",
	"┣━┫┗┳┛┣━┛┣━┫┣━┫┣╸ ",
	"╹ ╹ ╹ ╹  ╹ ╹╹ ╹┗━╸",
}

// renderWelcome draws this view's mycelium banner (cv.welcomeArt, generated once
// per ChatView so each new session tab looks different), centred on the wordmark's
// focal column so the organic, asymmetric threads can extend freely to either side.
func (cv *ChatView) renderWelcome(b *strings.Builder, width int) {
	_, _, _, viewH := cv.GetInnerRect()
	const subtitle = "terminal coding agent"

	// art + blank + subtitle + blank + hint
	const extraRows = 4
	for range max(0, (viewH-len(cv.welcomeArt)-extraRows)/2) {
		b.WriteByte('\n')
	}

	// Centre on the wordmark's focal column rather than the (asymmetric) bounding box.
	pad := strings.Repeat(" ", max(0, width/2-cv.welcomeFocal))
	// Vertical blue→purple gradient across the mycelium (Accent → ApexColor).
	n := len(cv.welcomeArt)
	for i, line := range cv.welcomeArt {
		t := 0.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		fmt.Fprintf(b, "[%s]%s%s[-]\n", lerpColor(Theme.Accent, Theme.ApexColor, t), pad, line)
	}

	b.WriteByte('\n')
	subPad := strings.Repeat(" ", max(0, (width-tview.TaggedStringWidth(subtitle))/2))
	fmt.Fprintf(b, "[%s]%s%s[-]\n", TC.Muted, subPad, subtitle)

	b.WriteByte('\n')
	hint := fmt.Sprintf("[%s]Ctrl+P[%s]  Commands   ·   [%s]Ctrl+D[%s]  Quit", TC.Accent, TC.Muted, TC.Accent, TC.Muted)
	hintPad := strings.Repeat(" ", max(0, (width-tview.TaggedStringWidth(hint))/2))
	fmt.Fprintf(b, "%s%s[-]\n", hintPad, hint)
}

// lerpColor linearly interpolates between two colors (t in [0,1]) and returns a
// tview hex color-tag value like "#82aaff".
func lerpColor(a, c tcell.Color, t float64) string {
	ar, ag, ab := a.RGB()
	cr, cg, cb := c.RGB()
	mix := func(x, y int32) int { return int(float64(x) + (float64(y)-float64(x))*t) }
	return fmt.Sprintf("#%02x%02x%02x", mix(ar, cr), mix(ag, cg), mix(ab, cb))
}

// ─── mycelium generator ──────────────────────────────────────────────────────

// connection bits for a cell (which neighbours a thread links to).
const (
	dN = 1
	dE = 2
	dS = 4
	dW = 8
)

var (
	hyphaeStep = map[int][2]int{dN: {-1, 0}, dE: {0, 1}, dS: {1, 0}, dW: {0, -1}}
	hyphaeOpp  = map[int]int{dN: dS, dS: dN, dE: dW, dW: dE}
	// glyphOf maps a cell's connection bitmask to its box-drawing rune.
	hyphaeGlyph = map[int]rune{
		0: ' ', dN: '╹', dE: '╺', dS: '╻', dW: '╸',
		dN | dS: '│', dE | dW: '─',
		dN | dE: '└', dN | dW: '┘', dS | dE: '┌', dS | dW: '┐',
		dN | dE | dW: '┴', dS | dE | dW: '┬', dN | dS | dE: '├', dN | dS | dW: '┤',
		dN | dS | dE | dW: '┼',
	}
)

// mycelium is a working canvas for growing one hyphal network.
type mycelium struct {
	conn [][]int
	tip  [][]bool
	r    *rand.Rand
}

func (m *mycelium) inb(y, x int) bool {
	return y >= 0 && y < hyphaeCanvasH && x >= 0 && x < hyphaeCanvasW
}

// forbidden marks the central clearing reserved for the wordmark.
func (m *mycelium) forbidden(y, x int) bool {
	cy := (hyphaeCanvasH - 1) / 2
	return y >= cy-1 && y <= cy+1 && x >= 12 && x <= 35
}

func (m *mycelium) link(y, x, d int) (int, int, bool) {
	ny, nx := y+hyphaeStep[d][0], x+hyphaeStep[d][1]
	if !m.inb(ny, nx) || m.forbidden(ny, nx) {
		return 0, 0, false
	}
	m.conn[y][x] |= d
	m.conn[ny][nx] |= hyphaeOpp[d]
	return ny, nx, true
}

// walk grows one filament outward with a meandering, branching path.
func (m *mycelium) walk(y, x, dir, length, depth int) {
	cx, cy := float64(hyphaeCanvasW-1)/2, float64(hyphaeCanvasH-1)/2
	for i := 0; i < length; i++ {
		// mostly continue straight, occasionally turn ±90°, biased outward
		best, bestScore := dir, math.Inf(-1)
		for _, d := range m.shuffle([]int{dir, dir, dir, hyphaeTurnL(dir), hyphaeTurnR(dir)}) {
			ny, nx := y+hyphaeStep[d][0], x+hyphaeStep[d][1]
			if !m.inb(ny, nx) || m.forbidden(ny, nx) {
				continue
			}
			score := math.Hypot(float64(nx)-cx, (float64(ny)-cy)*2.2) + m.r.Float64()*1.5
			if d == dir {
				score += 1.2 // momentum
			}
			if score > bestScore {
				bestScore, best = score, d
			}
		}
		ny, nx, ok := m.link(y, x, best)
		if !ok {
			m.tip[y][x] = true
			return
		}
		y, x, dir = ny, nx, best
		if depth < 3 && i >= 1 && m.r.Float64() < 0.28 {
			bd := hyphaeTurnL(dir)
			if m.r.Intn(2) == 0 {
				bd = hyphaeTurnR(dir)
			}
			bl := max(2, length-i-1)
			m.walk(y, x, bd, bl, depth+1)
		}
	}
	m.tip[y][x] = true
}

func hyphaeTurnL(d int) int {
	switch d {
	case dN:
		return dW
	case dW:
		return dS
	case dS:
		return dE
	case dE:
		return dN
	}
	return d
}

func hyphaeTurnR(d int) int {
	switch d {
	case dN:
		return dE
	case dE:
		return dS
	case dS:
		return dW
	case dW:
		return dN
	}
	return d
}

func (m *mycelium) shuffle(s []int) []int {
	m.r.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
	return s
}

func bitCount(c int) int {
	n := 0
	for c > 0 {
		n += c & 1
		c >>= 1
	}
	return n
}

// grow seeds several filaments around the clearing and returns the occupied count.
func (m *mycelium) grow() int {
	cy, cx := (hyphaeCanvasH-1)/2, (hyphaeCanvasW-1)/2
	starts := []struct{ y, x, dir int }{
		{cy - 2, cx - 10, dW}, {cy - 2, cx + 10, dE},
		{cy + 2, cx - 10, dW}, {cy + 2, cx + 10, dE},
		{cy - 2, cx - 6, dN}, {cy - 2, cx + 6, dN},
		{cy + 2, cx - 6, dS}, {cy + 2, cx + 6, dS},
	}
	for _, s := range starts {
		if m.inb(s.y, s.x) {
			m.walk(s.y, s.x, s.dir, 7, 0)
		}
	}
	occupied := 0
	for y := 0; y < hyphaeCanvasH; y++ {
		for x := 0; x < hyphaeCanvasW; x++ {
			if m.conn[y][x] != 0 {
				occupied++
			}
		}
	}
	return occupied
}

func (m *mycelium) render() []string {
	rows := make([]string, hyphaeCanvasH)
	for y := 0; y < hyphaeCanvasH; y++ {
		var b strings.Builder
		for x := 0; x < hyphaeCanvasW; x++ {
			c := m.conn[y][x]
			ch := hyphaeGlyph[c]
			if m.tip[y][x] && bitCount(c) <= 1 {
				ch = '▪' // hyphal growing tip
			}
			b.WriteRune(ch)
		}
		rows[y] = strings.TrimRight(b.String(), " ")
	}
	return rows
}

// buildHyphaeArt generates one mycelium network with the wordmark spliced into
// the central clearing. It returns the rows and the wordmark's centre column.
// It retries a few seeds so a sparse random draw never ships an empty-looking art.
func buildHyphaeArt(seed int64) ([]string, int) {
	r := rand.New(rand.NewSource(seed))
	var rows []string
	for attempt := 0; attempt < 24; attempt++ {
		m := &mycelium{
			conn: make([][]int, hyphaeCanvasH),
			tip:  make([][]bool, hyphaeCanvasH),
			r:    r,
		}
		for i := range m.conn {
			m.conn[i] = make([]int, hyphaeCanvasW)
			m.tip[i] = make([]bool, hyphaeCanvasW)
		}
		if m.grow() >= 46 {
			rows = m.render()
			break
		}
	}
	if rows == nil { // extremely unlikely; fall back to the last draw
		rows = make([]string, hyphaeCanvasH)
	}

	cy := (hyphaeCanvasH - 1) / 2
	wmStart := (hyphaeCanvasW - len([]rune(hyphaeWordmark[0]))) / 2
	for k, wl := range hyphaeWordmark {
		y := cy - 1 + k
		row := []rune(fmt.Sprintf("%-*s", hyphaeCanvasW, rows[y]))
		for j, c := range []rune(wl) {
			row[wmStart+j] = c
		}
		rows[y] = strings.TrimRight(string(row), " ")
	}
	focal := wmStart + len([]rune(hyphaeWordmark[0]))/2
	return rows, focal
}
