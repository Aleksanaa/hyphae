package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aleksana/hypane/internal/llm"
)

// toolDef pairs a schema (sent to the LLM) with an executor (run locally).
type toolDef struct {
	schema  llm.Tool
	execute func(ctx context.Context, args map[string]any, workDir string) (string, error)
}

var builtinTools = []toolDef{
	{
		schema: llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "read_file",
				Description: "Read the contents of a file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "File path (absolute or relative to working directory)"},
					},
					"required": []string{"path"},
				},
			},
		},
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			path := resolvePath(str(args, "path"), workDir)
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	},
	{
		schema: llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "write_file",
				Description: "Write content to a file, creating it or overwriting it.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "File path"},
						"content": map[string]any{"type": "string", "description": "Content to write"},
					},
					"required": []string{"path", "content"},
				},
			},
		},
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			path := resolvePath(str(args, "path"), workDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(path, []byte(str(args, "content")), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(str(args, "content")), path), nil
		},
	},
	{
		schema: llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "list_directory",
				Description: "List the contents of a directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Directory path (defaults to working directory)"},
					},
					"required": []string{},
				},
			},
		},
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			p := str(args, "path")
			if p == "" {
				p = workDir
			} else {
				p = resolvePath(p, workDir)
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			for _, e := range entries {
				if e.IsDir() {
					fmt.Fprintf(&b, "%s/\n", e.Name())
				} else {
					info, _ := e.Info()
					if info != nil {
						fmt.Fprintf(&b, "%s (%d bytes)\n", e.Name(), info.Size())
					} else {
						fmt.Fprintf(&b, "%s\n", e.Name())
					}
				}
			}
			return b.String(), nil
		},
	},
	{
		schema: llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "run_shell",
				Description: "Execute a shell command and return its output. Runs in the working directory.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"command":   map[string]any{"type": "string", "description": "Shell command to run"},
						"reasoning": map[string]any{"type": "string", "description": "One short sentence explaining why this command is being run"},
					},
					"required": []string{"command", "reasoning"},
				},
			},
		},
		execute: func(ctx context.Context, args map[string]any, workDir string) (string, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", str(args, "command"))
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				// Return output + error (non-zero exit is useful info, not a failure)
				return string(out) + "\n[exit error: " + err.Error() + "]", nil
			}
			return string(out), nil
		},
	},
	{
		schema: llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        "search_files",
				Description: "Search for a text pattern across files. Returns matching lines with file names.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pattern": map[string]any{"type": "string", "description": "Text or regex pattern to search for"},
						"path":    map[string]any{"type": "string", "description": "Directory or file to search (defaults to working directory)"},
					},
					"required": []string{"pattern"},
				},
			},
		},
		execute: func(ctx context.Context, args map[string]any, workDir string) (string, error) {
			p := str(args, "path")
			if p == "" {
				p = workDir
			} else {
				p = resolvePath(p, workDir)
			}
			cmd := exec.CommandContext(ctx, "grep", "-rn", "--include=*", str(args, "pattern"), p)
			cmd.Dir = workDir
			out, _ := cmd.CombinedOutput()
			if len(out) == 0 {
				return "(no matches)", nil
			}
			return string(out), nil
		},
	},
}

// schemas returns the Tool schemas for the LLM request.
func schemas() []llm.Tool {
	out := make([]llm.Tool, len(builtinTools))
	for i, t := range builtinTools {
		out[i] = t.schema
	}
	return out
}

// executeTool runs the named tool and returns its output.
func executeTool(ctx context.Context, name, argsJSON, workDir string) (string, bool) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}
	for _, t := range builtinTools {
		if t.schema.Function.Name == name {
			out, err := t.execute(ctx, args, workDir)
			if err != nil {
				return err.Error(), true
			}
			return out, false
		}
	}
	return fmt.Sprintf("unknown tool: %s", name), true
}

func resolvePath(p, workDir string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
