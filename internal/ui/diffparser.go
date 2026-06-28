// Diff line parsing adapted from differ (https://github.com/jansmrcka/differ)
// by Jan Smrčka — MIT License (Copyright (c) 2026 Jan Smrčka).
package ui

import (
	"fmt"
	"strconv"
	"strings"
)

// DiffLineType classifies a line in a unified diff.
type DiffLineType int

const (
	DiffContext DiffLineType = iota
	DiffAdded
	DiffRemoved
	DiffHunkHeader
)

// DiffLine is a single parsed line from a unified diff.
type DiffLine struct {
	Type    DiffLineType
	Content string
	OldNum  int
	NewNum  int
}

const maxDiffViewLines = 5000

// ParseUnifiedDiff parses raw unified diff text into structured lines.
func ParseUnifiedDiff(raw string) []DiffLine {
	var lines []DiffLine
	oldNum, newNum := 0, 0
	for _, line := range strings.Split(raw, "\n") {
		if len(lines) >= maxDiffViewLines {
			lines = append(lines, DiffLine{
				Type: DiffHunkHeader, Content: fmt.Sprintf("… truncated (%d+ lines)", maxDiffViewLines),
				OldNum: -1, NewNum: -1,
			})
			break
		}
		if dl := parseDiffLine(line, &oldNum, &newNum); dl != nil {
			lines = append(lines, *dl)
		}
	}
	return lines
}

func parseDiffLine(line string, oldNum, newNum *int) *DiffLine {
	switch {
	case strings.HasPrefix(line, "diff "),
		strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "new file"),
		strings.HasPrefix(line, "deleted file"),
		strings.HasPrefix(line, "--- "),
		strings.HasPrefix(line, "+++ "):
		return nil
	case strings.HasPrefix(line, "@@"):
		parseDiffHunkHeader(line, oldNum, newNum)
		return &DiffLine{Type: DiffHunkHeader, Content: extractHunkCtx(line), OldNum: -1, NewNum: -1}
	case strings.HasPrefix(line, "+"):
		dl := &DiffLine{Type: DiffAdded, Content: line[1:], OldNum: -1, NewNum: *newNum}
		*newNum++
		return dl
	case strings.HasPrefix(line, "-"):
		dl := &DiffLine{Type: DiffRemoved, Content: line[1:], OldNum: *oldNum, NewNum: -1}
		*oldNum++
		return dl
	case strings.HasPrefix(line, `\`), line == "":
		return nil
	default:
		content := line
		if strings.HasPrefix(line, " ") {
			content = line[1:]
		}
		dl := &DiffLine{Type: DiffContext, Content: content, OldNum: *oldNum, NewNum: *newNum}
		*oldNum++
		*newNum++
		return dl
	}
}

func extractHunkCtx(line string) string {
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) == 3 {
		if ctx := strings.TrimSpace(parts[2]); ctx != "" {
			return ctx
		}
	}
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[1])
	}
	return line
}

func parseDiffHunkHeader(line string, oldNum, newNum *int) {
	parts := strings.SplitN(line, "@@", 3)
	if len(parts) < 2 {
		return
	}
	for _, r := range strings.Fields(strings.TrimSpace(parts[1])) {
		if strings.HasPrefix(r, "-") {
			nums := strings.SplitN(r[1:], ",", 2)
			if n, err := strconv.Atoi(nums[0]); err == nil {
				*oldNum = n
			}
		} else if strings.HasPrefix(r, "+") {
			nums := strings.SplitN(r[1:], ",", 2)
			if n, err := strconv.Atoi(nums[0]); err == nil {
				*newNum = n
			}
		}
	}
}
