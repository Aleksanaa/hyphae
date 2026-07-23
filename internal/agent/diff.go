package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// computeDiffForApproval pre-computes a unified diff for write_file/edit_file
// before the approval prompt is shown. Returns a non-nil error when the edit
// cannot be applied (e.g. old_string not found), in which case the caller
// should auto-fail the tool call without showing the approval prompt.
func computeDiffForApproval(toolName string, args map[string]any, workDir string) (filePath, patch string, err error) {
	switch toolName {
	case "write_file":
		fp, p := computeWriteDiff(args, workDir)
		return fp, p, nil
	case "edit_file":
		return computeEditDiff(args, workDir)
	}
	return "", "", nil
}

func computeWriteDiff(args map[string]any, workDir string) (filePath, patch string) {
	rel := str(args, "path")
	path := resolvePath(rel, workDir)
	newContent := str(args, "content")

	oldContent := ""
	if b, err := os.ReadFile(path); err == nil {
		oldContent = string(b)
	}
	return rel, generateUnifiedDiff(oldContent, newContent, rel)
}

func computeEditDiff(args map[string]any, workDir string) (filePath, patch string, err error) {
	rel := str(args, "path")
	path := resolvePath(rel, workDir)

	b, err := os.ReadFile(path)
	if err != nil {
		return rel, "", err
	}
	oldContent := string(b)
	newContent := oldContent

	edits, _ := args["edits"].([]any)
	if len(edits) == 0 {
		return rel, "", fmt.Errorf("edits must be a non-empty array")
	}
	for i, e := range edits {
		em, ok := e.(map[string]any)
		if !ok {
			return rel, "", fmt.Errorf("edit %d: invalid format", i)
		}
		oldStr := str(em, "old_string")
		newStr := str(em, "new_string")
		count := strings.Count(newContent, oldStr)
		if count == 0 {
			return rel, "", fmt.Errorf("edit %d: old_string not found", i)
		}
		if count > 1 {
			return rel, "", fmt.Errorf("edit %d: old_string appears %d times — add more context to make it unique", i, count)
		}
		newContent = strings.Replace(newContent, oldStr, newStr, 1)
	}
	return rel, generateUnifiedDiff(oldContent, newContent, rel), nil
}

// generateUnifiedDiff returns a unified diff of old vs new content.
func generateUnifiedDiff(oldContent, newContent, label string) string {
	if oldContent == newContent {
		return ""
	}
	return udiff.Unified("a/"+label, "b/"+label, oldContent, newContent)
}
