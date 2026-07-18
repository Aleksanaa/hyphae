package ui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rivo/tview"
	"github.com/rivo/uniseg"

	"github.com/aleksanaa/hyphae/internal/session"
)

// renderMsg is the UI's view of one rendered entry, decoupled from session.Message
// (which no longer carries box-styling fields). role reuses session.Role values;
// a status role (RoleThinking/RoleTool) marks a flat status line, while expandedBox
// marks a status box. This keeps the box renderer and selection code role-driven.
type renderMsg struct {
	role          session.Role
	content       string
	err           error
	partial       bool
	expandedBox   bool
	boxTitle      string
	fullWidth     bool
	contentTagged bool
}

// stripTags removes tview color/attribute tags and restores escaped brackets.
// tview.Escape converts [x] → [x[] so inner content containing '[' marks an
// escape: the text before the '[' plus a literal ']' is the original sequence.
func stripTags(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		idx := strings.IndexByte(s, '[')
		if idx < 0 {
			out.WriteString(s)
			return out.String()
		}
		out.WriteString(s[:idx])
		s = s[idx:]
		end := strings.IndexByte(s[1:], ']')
		if end < 0 {
			out.WriteByte('[')
			s = s[1:]
			continue
		}
		inner := s[1 : end+1]
		if i := strings.LastIndexByte(inner, '['); i >= 0 {
			// Escaped bracket sequence: emit the text before '[' wrapped in [ ].
			out.WriteByte('[')
			out.WriteString(inner[:i])
			out.WriteByte(']')
		}
		// Otherwise it's a color/attribute tag — consume silently.
		s = s[end+2:]
	}
	return out.String()
}

const riLo, riHi = 0x1F1E6, 0x1F1FF // regional indicator symbols 🇦..🇿

// unstableRune reports whether r can take part in a multi-codepoint emoji cluster
// whose rendered width terminals disagree on: ZWJ, variation selector 16, the
// enclosing keycap, and anything in the emoji/supplementary planes (which covers
// regional indicators, skin-tone modifiers, tag characters and emoji bases).
func unstableRune(r rune) bool {
	return r == 0x200D || r == 0xFE0F || r == 0x20E3 || r >= 0x1F000
}

// stabilizeWidth rewrites multi-codepoint emoji grapheme clusters — whose
// terminal-rendered width routinely disagrees with the Unicode width (2) that
// tcell reserves — into a single width-stable representative, keeping the
// message-box borders aligned on every terminal:
//
//   - regional-indicator flags (🇨🇳)                    → ISO letters (CN)
//   - keycaps (1️⃣)                                      → the base digit (1)
//   - ZWJ (👨‍👩‍👧‍👦), skin-tone (👍🏽), variation-selector (❤️)
//     and tag-sequence flags (🏴…)                      → their base emoji
//
// Plain text, CJK, and letter+diacritic clusters are left untouched. Applied at
// render time only; the stored message keeps the original characters.
func stabilizeWidth(s string) string {
	// Fast path: nothing that can form an unstable cluster.
	if strings.IndexFunc(s, unstableRune) < 0 {
		return s
	}
	isRI := func(r rune) bool { return r >= riLo && r <= riHi }

	var b strings.Builder
	b.Grow(len(s))
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		rs := g.Runes()
		if len(rs) == 1 {
			b.WriteRune(rs[0])
			continue
		}
		// Multi-rune cluster: simplify only genuine emoji sequences, so that
		// combining-mark clusters (e.g. "e"+◌́) are preserved intact.
		emoji, allRI := false, true
		for _, r := range rs {
			if unstableRune(r) {
				emoji = true
			}
			if !isRI(r) {
				allRI = false
			}
		}
		switch {
		case !emoji:
			b.WriteString(g.Str())
		case allRI:
			for _, r := range rs {
				b.WriteRune('A' + (r - riLo))
			}
		default:
			b.WriteRune(rs[0]) // keep the base emoji / digit / symbol
		}
	}
	return b.String()
}

// ─── message grouping ────────────────────────────────────────────────────────

type renderItemKind int

const (
	riCompactDivider  renderItemKind = iota // compact separator line or summary box
	riCollapsedRounds                       // ≥1 consecutive settled status items collapsed into one apex entry
	riLiveStatus                            // in-progress status (streaming thinking / running tool)
	riMessage                               // user or assistant message with content or error
)

// renderItem is one logical unit to be rendered in the conversation.
type renderItem struct {
	kind renderItemKind

	divIdx int // riCompactDivider: index into compactSeqs

	// riCollapsedRounds: the run of consecutive settled status items.
	firstStatusIdx int
	statuses       []session.Message

	// riLiveStatus
	liveMsgIdx  int
	liveContent string // from the status's own events/secs; liveStatus fallback applied in buildText

	// riMessage
	msg    session.Message
	msgIdx int
}

// statusInProgress reports whether a status item is still live: thinking that is
// streaming, or a tool call awaiting approval or running.
func statusInProgress(m session.Message) bool {
	if m.Partial {
		return true
	}
	for _, tu := range m.ToolUses {
		if tu.State == "running" || tu.State == "pending" {
			return true
		}
	}
	return false
}

// formatStatusEvent renders a single op event as live progress text.
func formatStatusEvent(ev session.StatusEvent) string {
	switch ev.Kind {
	case session.StatusEventDoing:
		return ev.Verb + " " + ev.Target
	case session.StatusEventWants:
		return "wants to " + ev.Verb + " " + ev.Target
	case session.StatusEventDone:
		return ev.Verb + " " + ev.Target
	case session.StatusEventRefused:
		return "wanted to " + ev.Verb + " " + ev.Target
	case session.StatusEventFailed:
		return "failed to " + ev.Verb + " " + ev.Target
	}
	return ""
}

// liveStatusText builds the progress line for an in-progress status from its own
// thinking duration and latest op event. Empty when there is nothing yet — the
// caller falls back to the transient liveStatus label.
func liveStatusText(m session.Message) string {
	var thinkText string
	if m.Role == session.RoleThinking && m.ThinkingSecs > 0 {
		thinkText = fmt.Sprintf("thought for %ds", m.ThinkingSecs)
	}
	var opsText string
	for _, ev := range m.StatusEvents {
		opsText = formatStatusEvent(ev)
	}
	switch {
	case thinkText != "" && opsText != "":
		return thinkText + ", " + opsText
	case thinkText != "":
		return thinkText
	default:
		return opsText
	}
}

// toolCode extracts the Starlark code and output from a tool status item.
func toolCode(m session.Message) (code, output string) {
	if len(m.ToolUses) == 0 {
		return "", ""
	}
	var args struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(m.ToolUses[0].Input), &args) == nil {
		code = args.Code
	}
	return code, m.ToolUses[0].Output
}

// collapseStatuses summarizes a run of settled status items into a one-line
// description ("thought for 3s, read 2 files") and the total thinking duration
// used for the expanded-box title.
func collapseStatuses(statuses []session.Message) (desc string, thinkSecs int) {
	anyThinking := false
	var allEvents []session.StatusEvent
	for _, s := range statuses {
		if s.Role == session.RoleThinking {
			thinkSecs += s.ThinkingSecs
			if s.Thinking != "" || s.ThinkingSecs > 0 {
				anyThinking = true
			}
		} else {
			allEvents = append(allEvents, s.StatusEvents...)
		}
	}
	agg := aggregateOps(allEvents)
	if !anyThinking {
		return agg, 0
	}
	if thinkSecs > 0 {
		desc = fmt.Sprintf("thought for %ds", thinkSecs)
	} else {
		desc = "thought for a moment"
	}
	if agg != "" {
		desc += ", " + agg
	}
	return desc, thinkSecs
}

// collapsedDetail renders the expanded-box body for a run of statuses: each
// thinking status's text and each tool status's code/output box, blank-separated.
func collapsedDetail(statuses []session.Message, contentW int) string {
	var lines []string
	firstBlock := true
	blank := func() {
		if !firstBlock {
			lines = append(lines, "")
		}
		firstBlock = false
	}
	for _, s := range statuses {
		if s.Role == session.RoleThinking {
			if s.Thinking != "" {
				blank()
				lines = append(lines, tview.Escape(s.Thinking))
			}
		} else if code, output := toolCode(s); code != "" {
			blank()
			for _, rl := range renderCodeOutputLines(code, output, contentW) {
				lines = append(lines, rl.text)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// groupMessages converts the flat message list into ordered render items,
// interleaving compact dividers and collapsing consecutive settled status
// (thinking/tool) items into one apex entry. Because each round is already its
// own peer item, this is a single forward scan — no re-pairing of roles.
func groupMessages(msgs []session.Message, compactSeqs []int) []renderItem {
	var items []renderItem
	mn := len(msgs)
	nextDivider := 0

	flushDividers := func(before int) {
		for nextDivider < len(compactSeqs) && before > compactSeqs[nextDivider] {
			items = append(items, renderItem{kind: riCompactDivider, divIdx: nextDivider})
			nextDivider++
		}
	}

	for mi := 0; mi < mn; mi++ {
		flushDividers(mi)
		msg := msgs[mi]

		if msg.Role.IsStatus() {
			// An in-progress status renders live, not collapsed.
			if statusInProgress(msg) {
				items = append(items, renderItem{
					kind:        riLiveStatus,
					liveMsgIdx:  mi,
					liveContent: liveStatusText(msg),
				})
				continue
			}
			// Collect the run of consecutive settled status items.
			start := mi
			var statuses []session.Message
			for mi < mn && msgs[mi].Role.IsStatus() && !statusInProgress(msgs[mi]) {
				statuses = append(statuses, msgs[mi])
				mi++
			}
			mi-- // outer loop will re-increment
			items = append(items, renderItem{
				kind:           riCollapsedRounds,
				firstStatusIdx: start,
				statuses:       statuses,
			})
			continue
		}

		if msg.Role == session.RoleUser || msg.Content != "" || msg.Error != nil {
			items = append(items, renderItem{kind: riMessage, msg: msg, msgIdx: mi})
		}
	}

	// Flush any remaining dividers (e.g. compact just happened with no new messages yet).
	for nextDivider < len(compactSeqs) {
		items = append(items, renderItem{kind: riCompactDivider, divIdx: nextDivider})
		nextDivider++
	}
	return items
}

// aggregateOps groups Done and Failed status events by (kind, verb, noun), sums counts,
// and formats a summary. e.g. "read 2 files, failed to read 1 file, ran 3 functions"
func aggregateOps(events []session.StatusEvent) string {
	type key struct {
		kind        session.StatusEventKind
		verb, nounP string
	}
	type group struct {
		count int
		last  string
	}
	var order []key
	groups := map[key]*group{}
	for _, ev := range events {
		if ev.Kind != session.StatusEventDone && ev.Kind != session.StatusEventFailed {
			continue
		}
		k := key{ev.Kind, ev.Verb, ev.NounP}
		if g, ok := groups[k]; ok {
			g.count++
			g.last = ev.Target
		} else {
			order = append(order, k)
			groups[k] = &group{1, ev.Target}
		}
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, len(order))
	for i, k := range order {
		g := groups[k]
		prefix := ""
		if k.kind == session.StatusEventFailed {
			prefix = "failed to "
		}
		if g.count == 1 {
			parts[i] = prefix + k.verb + " " + g.last
		} else {
			parts[i] = fmt.Sprintf("%s%s %d %s", prefix, k.verb, g.count, k.nounP)
		}
	}
	return strings.Join(parts, ", ")
}

// renderCodeOutputLines produces a bordered box containing syntax-highlighted
// Starlark code and its output for embedding inside an ExpandedBox.
// maxW is the inner content width of the containing ExpandedBox.
// Code is capped at 20 lines; output at 10 lines.
func renderCodeOutputLines(code, output string, maxW int) []renderedLine {
	const codeMaxLines = 20
	const outputMaxLines = 10

	bc := TC.Border
	mc := TC.Muted

	innerW := max(1, maxW-4)
	fill := func(n int) string { return strings.Repeat("─", max(0, n)) }

	wrapLine := func(rl renderedLine) renderedLine {
		visW := tview.TaggedStringWidth(rl.text)
		pad := max(0, innerW-visW)
		text := fmt.Sprintf("[%s]│[-:-:-] %s%s [%s]│[-:-:-]", bc, rl.text, strings.Repeat(" ", pad), bc)
		totalW := 2 + visW + pad + 2
		mask := make([]bool, totalW)
		for col := 2; col < 2+visW; col++ {
			mask[col] = true
		}
		return renderedLine{text: text, copyMask: mask, softWrap: rl.softWrap}
	}

	var out []renderedLine

	// Top border: ┌──────────────────── input ─┐ (right-aligned)
	{
		inLabel := "input"
		leftFill := max(0, maxW-len([]rune(inLabel))-5)
		top := fmt.Sprintf("[%s]┌%s [%s]%s [%s]─┐[-:-:-]", bc, fill(leftFill), mc, inLabel, bc)
		out = append(out, renderedLine{text: top, copyMask: make([]bool, tview.TaggedStringWidth(top))})
	}

	// Code lines with python highlighting
	allCodeLines := strings.Split(code, "\n")
	truncatedCode := len(allCodeLines) > codeMaxLines
	codeLines := allCodeLines
	if truncatedCode {
		codeLines = allCodeLines[:codeMaxLines]
	}
	cb := &codeBlock{lang: "python", lines: codeLines}
	for _, rl := range cb.renderHighlighted(innerW) {
		out = append(out, wrapLine(rl))
	}
	if truncatedCode {
		trunc := fmt.Sprintf("[%s]… %d more lines[-:-:-]", mc, len(allCodeLines)-codeMaxLines)
		out = append(out, wrapLine(renderedLine{text: trunc}))
	}

	// Output section (omit trivial placeholder)
	if output != "" && output != "(done)" {
		// Middle separator: ├──────────────────── output ─┤ (right-aligned)
		outLabel := "output"
		leftFill := max(0, maxW-len([]rune(outLabel))-5)
		mid := fmt.Sprintf("[%s]├%s [%s]%s [%s]─┤[-:-:-]", bc, fill(leftFill), mc, outLabel, bc)
		out = append(out, renderedLine{text: mid, copyMask: make([]bool, tview.TaggedStringWidth(mid))})

		allOutLines := strings.Split(output, "\n")
		truncatedOut := len(allOutLines) > outputMaxLines
		outLines := allOutLines
		if truncatedOut {
			outLines = allOutLines[:outputMaxLines]
		}
		cb2 := &codeBlock{lines: outLines}
		for _, rl := range cb2.renderPlain(innerW) {
			out = append(out, wrapLine(rl))
		}
		if truncatedOut {
			trunc := fmt.Sprintf("[%s]… %d more lines[-:-:-]", mc, len(allOutLines)-outputMaxLines)
			out = append(out, wrapLine(renderedLine{text: trunc}))
		}
	}

	// Bottom border: └────────────────────────────────────────────┘
	bot := fmt.Sprintf("[%s]└%s┘[-:-:-]", bc, fill(maxW-2))
	out = append(out, renderedLine{text: bot, copyMask: make([]bool, tview.TaggedStringWidth(bot))})

	return out
}

// ─── single-message rendering ────────────────────────────────────────────────

// maxTaggedWidth returns the maximum visual width across tview-tagged lines.
func maxTaggedWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := tview.TaggedStringWidth(l); n > w {
			w = n
		}
	}
	return w
}

// renderMessageBox writes a compact bordered box into b and returns leftPad and boxW.
// Box anatomy:  ┌─ label ───┐ / │ content │ / └───────────┘
// expandedBox uses dotted borders (╌/╎) with boxTitle; boxTitle alone uses solid borders
// with a centered title; otherwise solid borders with the "apex" label.
func (cv *ChatView) renderMessageBox(b *strings.Builder, msg renderMsg, lay convoLayout, isHovered bool) (leftPad, boxW int) {
	bc := Theme.Border.CSS()
	if isHovered {
		bc = Theme.BorderFocus.CSS()
	}

	hChar, vChar := "─", "│"
	if msg.expandedBox {
		hChar, vChar = "╌", "╎"
	}
	fill := func(n int) string { return strings.Repeat(hChar, max(0, n)) }
	// mkLine pads inner to contentW; [-:-:-] resets style before padding so
	// trailing spaces and the border char are not colored by inner tags.
	mkLine := func(contentW int) func(string, int) string {
		return func(inner string, vlen int) string {
			return fmt.Sprintf("[%s]%s[-] %s[-:-:-]%s [%s]%s[-]", bc, vChar, inner, strings.Repeat(" ", max(0, contentW-vlen)), bc, vChar)
		}
	}

	switch msg.role {
	case session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.content), lay.userW-4)
		boxW = max(8, maxTaggedWidth(lines)+4)
		// Right-align within the band; the capped width leaves ≥20% blank on the band's left.
		leftPad = lay.off + max(0, lay.band-boxW)
		p := strings.Repeat(" ", leftPad)
		boxLine := mkLine(boxW - 4)
		fmt.Fprintf(b, "%s[%s]┌─ [%s]you [%s]%s┐[-]\n", p, bc, TC.UserColor, bc, fill(boxW-8))
		for _, line := range lines {
			fmt.Fprintf(b, "%s%s\n", p, boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))

	case session.RoleAssistant:
		// Assistant boxes hug the band's left edge; their capped width leaves the right margin.
		// Full-width boxes (e.g. the compact summary) span the whole band instead.
		maxContentW := lay.asstW - 4
		if msg.fullWidth {
			maxContentW = lay.band - 4
		}
		leftPad = lay.off
		p := strings.Repeat(" ", lay.off)
		if msg.err != nil {
			lines := tview.WordWrap(tview.Escape(msg.err.Error()), maxContentW)
			boxW = max(10, maxTaggedWidth(lines)+4)
			boxLine := mkLine(boxW - 4)
			fmt.Fprintf(b, "%s[%s]┌─ [%s]error [%s]%s┐[-]\n", p, bc, TC.ErrorColor, bc, fill(boxW-10))
			for _, line := range lines {
				fmt.Fprintf(b, "%s%s\n", p, boxLine(fmt.Sprintf("[%s]%s[-]", TC.ErrorColor, line), tview.TaggedStringWidth(line)))
			}
			fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))
			return
		}

		var lines []string
		if msg.expandedBox {
			content := msg.content
			if !msg.contentTagged {
				content = tview.Escape(content)
			}
			lines = tview.WordWrap(content, maxContentW)
		} else {
			blocks, ok := cv.mdCache[msg.content]
			if !ok {
				blocks = parseMarkdown(msg.content)
				cv.mdCache[msg.content] = blocks
			}
			lines = renderBlocks(blocks, maxContentW)
		}

		partialFrag, extraW := "", 0
		if msg.partial {
			partialFrag = fmt.Sprintf("[%s]… [-]", TC.Muted)
			extraW = 2
		}
		minBoxW := 9
		if msg.boxTitle != "" {
			minBoxW = tview.TaggedStringWidth(msg.boxTitle) + 5
		} else if msg.partial {
			minBoxW = 11
		}
		if msg.fullWidth {
			boxW = lay.band
		} else {
			boxW = max(minBoxW+extraW, maxTaggedWidth(lines)+4)
		}
		boxLine := mkLine(boxW - 4)

		if msg.expandedBox {
			fmt.Fprintf(b, "%s[%s]┌╌ %s %s[%s]%s┐[-]\n",
				p, bc, msg.boxTitle, partialFrag, bc, fill(boxW-tview.TaggedStringWidth(msg.boxTitle)-5-extraW))
			if len(lines) == 0 {
				fmt.Fprintf(b, "%s%s\n", p, boxLine("", 0))
			}
		} else if msg.boxTitle != "" {
			titleTagW := tview.TaggedStringWidth(msg.boxTitle)
			inner := boxW - titleTagW - 4 // total dash space
			leftF := inner / 2
			rightF := inner - leftF
			fmt.Fprintf(b, "%s[%s]┌%s %s [%s]%s┐[-]\n",
				p, bc, fill(leftF), msg.boxTitle, bc, fill(rightF))
		} else {
			fmt.Fprintf(b, "%s[%s]┌─ [%s]apex [%s]%s[%s]%s┐[-]\n",
				p, bc, TC.ApexColor, bc, partialFrag, bc, fill(boxW-9-extraW))
		}
		for _, line := range lines {
			fmt.Fprintf(b, "%s%s\n", p, boxLine(line, tview.TaggedStringWidth(line)))
		}
		fmt.Fprintf(b, "%s[%s]└%s┘[-]\n", p, bc, fill(boxW-2))
	}
	return
}

// apexLabel wraps desc in the dim "apex" prefix with muted text color.
func apexLabel(desc string) string {
	return fmt.Sprintf("[%s]apex[-][%s] %s[-]", TC.ApexDim, TC.Muted, desc)
}

// computeMsgContent returns the plain-text content lines for a rendered entry,
// wrapped to the same per-role column width used when rendering (asstW for
// assistant/status content, userW for user messages). The second return is a
// legacy indent hint kept for callers that still read it.
func computeMsgContent(msg renderMsg, lay convoLayout) ([]string, int) {
	switch {
	case msg.role == session.RoleUser:
		lines := tview.WordWrap(tview.Escape(msg.content), lay.userW-4)
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, 0
	case msg.role == session.RoleAssistant:
		cw := lay.asstW - 4
		if msg.fullWidth {
			cw = lay.band - 4
		}
		var plain string
		switch {
		case msg.expandedBox:
			plain = msg.content
		case msg.err != nil:
			plain = msg.err.Error()
		default:
			raw := renderMarkdown(msg.content, cw)
			out := make([]string, len(raw))
			for i, l := range raw {
				out[i] = stripTags(l)
			}
			return out, 0
		}
		lines := tview.WordWrap(tview.Escape(plain), cw)
		for i, l := range lines {
			lines[i] = tview.Unescape(l)
		}
		return lines, 0
	case msg.role.IsStatus():
		return []string{stripTags(msg.content)}, 2
	}
	return nil, 0
}
