package agent

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// computeDiffForApproval pre-computes a unified diff for write_file/edit_file
// before the approval prompt is shown. Returns empty strings for other tools.
func computeDiffForApproval(toolName, argsJSON, workDir string) (filePath, patch string) {
	switch toolName {
	case "write_file":
		return computeWriteDiff(argsJSON, workDir)
	case "edit_file":
		return computeEditDiff(argsJSON, workDir)
	}
	return "", ""
}

func computeWriteDiff(argsJSON, workDir string) (filePath, patch string) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", ""
	}
	rel := str(args, "path")
	path := resolvePath(rel, workDir)
	newContent := str(args, "content")

	oldContent := ""
	if b, err := os.ReadFile(path); err == nil {
		oldContent = string(b)
	}
	return rel, generateUnifiedDiff(oldContent, newContent, rel)
}

func computeEditDiff(argsJSON, workDir string) (filePath, patch string) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", ""
	}
	rel := str(args, "path")
	path := resolvePath(rel, workDir)

	b, err := os.ReadFile(path)
	if err != nil {
		return rel, ""
	}
	oldContent := string(b)
	newContent := oldContent

	edits, _ := args["edits"].([]any)
	for _, e := range edits {
		em, ok := e.(map[string]any)
		if !ok {
			continue
		}
		oldStr := str(em, "old_string")
		newStr := str(em, "new_string")
		if strings.Contains(newContent, oldStr) {
			newContent = strings.Replace(newContent, oldStr, newStr, 1)
		}
	}
	return rel, generateUnifiedDiff(oldContent, newContent, rel)
}

// generateUnifiedDiff returns a unified diff of old vs new content.
func generateUnifiedDiff(oldContent, newContent, label string) string {
	if oldContent == newContent {
		return ""
	}
	return udiff.Unified("a/"+label, "b/"+label, oldContent, newContent)
}
