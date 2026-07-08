package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	codespelunker "github.com/aleksanaa/hyphae/internal/third_party/codespelunker"
	"github.com/aleksanaa/hyphae/internal/third_party/codespelunker/ranker"
	"github.com/aleksanaa/hyphae/internal/third_party/codespelunker/snippet"
	openai "github.com/openai/openai-go/v3"
)

type toolDef struct {
	schema  openai.ChatCompletionToolUnionParam
	execute func(ctx context.Context, args map[string]any, workDir string) (string, error)
}

var builtinTools = []toolDef{
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "read_file",
			Description: openai.String("Read the contents of a file. Results are returned with line numbers (cat -n format). By default reads up to 2000 lines from the beginning. Use offset+limit to read a specific range within a large file."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":   map[string]any{"type": "string", "description": "Absolute or relative file path"},
					"offset": map[string]any{"type": "number", "description": "Line number to start reading from (1 = first line). Only provide when the file is too large to read at once."},
					"limit":  map[string]any{"type": "number", "description": "Maximum number of lines to read. Defaults to 2000."},
				},
				"required": []string{"path"},
			},
		}),
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			path := resolvePath(str(args, "path"), workDir)
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}

			lines := strings.Split(string(b), "\n")
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			total := len(lines)

			offset := 1
			if o, ok := args["offset"].(float64); ok && o >= 1 {
				offset = int(o)
			}
			limit := 2000
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}

			idx := offset - 1
			if idx >= total {
				return fmt.Sprintf("(past end of file — file has %d lines)", total), nil
			}
			end := idx + limit
			if end > total {
				end = total
			}

			var sb strings.Builder
			width := len(fmt.Sprintf("%d", total))
			for i, line := range lines[idx:end] {
				fmt.Fprintf(&sb, "%*d\t%s\n", width, idx+i+1, line)
			}
			if end < total {
				fmt.Fprintf(&sb, "\n(%d more lines — use offset=%d to continue)", total-end, end+1)
			}
			return sb.String(), nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "write_file",
			Description: openai.String("Write content to a file, creating it or overwriting it entirely. Prefer edit_file for modifying existing files."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path":      map[string]any{"type": "string", "description": "Absolute or relative file path"},
					"content":   map[string]any{"type": "string", "description": "Full content to write"},
					"reasoning": map[string]any{"type": "string", "description": "One short sentence explaining why this file is being written"},
				},
				"required": []string{"path", "content", "reasoning"},
			},
		}),
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			path := resolvePath(str(args, "path"), workDir)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", err
			}
			content := str(args, "content")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(content), path), nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "edit_file",
			Description: openai.String("Apply one or more exact-string replacements to a file. Each old_string must appear exactly once — include enough surrounding context to make it unique. Edits are applied in order. Use write_file to create new files."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Absolute or relative file path"},
					"edits": map[string]any{
						"type":        "array",
						"description": "List of replacements to apply in order.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_string": map[string]any{"type": "string", "description": "Exact string to find. Must be unique within the file."},
								"new_string": map[string]any{"type": "string", "description": "String to replace it with."},
							},
							"required": []string{"old_string", "new_string"},
						},
					},
					"reasoning": map[string]any{"type": "string", "description": "One short sentence explaining why this file is being edited"},
				},
				"required": []string{"path", "edits", "reasoning"},
			},
		}),
		execute: func(_ context.Context, args map[string]any, workDir string) (string, error) {
			path := resolvePath(str(args, "path"), workDir)
			b, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			edits, _ := args["edits"].([]any)
			if len(edits) == 0 {
				return "", fmt.Errorf("edits must be a non-empty array")
			}
			content := string(b)
			for i, e := range edits {
				em, ok := e.(map[string]any)
				if !ok {
					return "", fmt.Errorf("edit %d: invalid format", i)
				}
				oldStr := str(em, "old_string")
				newStr := str(em, "new_string")
				count := strings.Count(content, oldStr)
				if count == 0 {
					return "", fmt.Errorf("edit %d: old_string not found", i)
				}
				if count > 1 {
					return "", fmt.Errorf("edit %d: old_string appears %d times — add more context to make it unique", i, count)
				}
				content = strings.Replace(content, oldStr, newStr, 1)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("edited %s (%d replacements)", path, len(edits)), nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "list_directory",
			Description: openai.String("List the contents of a directory."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "Directory path (defaults to working directory)"},
				},
				"required": []string{},
			},
		}),
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
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "run_shell",
			Description: openai.String("Execute a shell command and return its output. Runs in the working directory."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"command":   map[string]any{"type": "string", "description": "Shell command to run"},
					"reasoning": map[string]any{"type": "string", "description": "One short sentence explaining why this command is being run"},
				},
				"required": []string{"command", "reasoning"},
			},
		}),
		execute: func(ctx context.Context, args map[string]any, workDir string) (string, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", str(args, "command"))
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out) + "\n[exit error: " + err.Error() + "]", nil
			}
			return string(out), nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "web_fetch",
			Description: openai.String("Fetch content from an HTTP or HTTPS URL and return it as text, markdown, or HTML. Markdown is the default. Use a more targeted tool when one is available."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"url":       map[string]any{"type": "string", "description": "The HTTP or HTTPS URL to fetch content from"},
					"format":    map[string]any{"type": "string", "enum": []string{"text", "markdown", "html"}, "description": "Output format. Defaults to markdown."},
					"timeout":   map[string]any{"type": "number", "description": "Optional timeout in seconds (max 120). Defaults to 30."},
					"reasoning": map[string]any{"type": "string", "description": "One short sentence explaining why this URL is being fetched"},
				},
				"required": []string{"url", "reasoning"},
			},
		}),
		execute: func(ctx context.Context, args map[string]any, workDir string) (string, error) {
			rawURL := str(args, "url")
			format := str(args, "format")
			if format == "" {
				format = "markdown"
			}
			timeout := 0
			if t, ok := args["timeout"].(float64); ok {
				timeout = int(t)
			}
			if rawURL == "" {
				return "", fmt.Errorf("url is required")
			}
			return fetchURL(ctx, rawURL, format, timeout)
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name: "search_files",
			Description: openai.String("Search for code or text across files using boolean query syntax. " +
				"Supports: AND (implicit between terms), OR, NOT, \"exact phrases\", /regex/, fuzzy~1, " +
				"and filters: ext:go, lang:Python, path:pkg, file:test. " +
				"Returns ranked matching lines with file names and line numbers."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"query":          map[string]any{"type": "string", "description": "Search query. Examples: \"func SendMessage\", \"error OR panic\", \"TODO NOT fixme\", \"ctx context.Context\" ext:go"},
					"path":           map[string]any{"type": "string", "description": "Directory to search (defaults to working directory)"},
					"case_sensitive": map[string]any{"type": "boolean", "description": "Whether the search is case-sensitive (default: false)"},
				},
				"required": []string{"query"},
			},
		}),
		execute: func(ctx context.Context, args map[string]any, workDir string) (string, error) {
			query := str(args, "query")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			p := str(args, "path")
			if p == "" {
				p = workDir
			} else {
				p = resolvePath(p, workDir)
			}
			caseSensitive, _ := args["case_sensitive"].(bool)

			results, _, err := codespelunker.Search(ctx, query, p, codespelunker.Options{
				CaseSensitive: caseSensitive,
			})
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "(no matches)", nil
			}

			testIntent := ranker.HasTestIntent(strings.Fields(query))
			results = ranker.RankResults("tfidf", len(results), results, nil, nil, testIntent)

			const maxFiles = 20
			if len(results) > maxFiles {
				results = results[:maxFiles]
			}

			var sb strings.Builder
			for _, res := range results {
				lines := snippet.FindAllMatchingLines(res, 200, 1, 0)
				if len(lines) == 0 {
					continue
				}
				rel, err := filepath.Rel(workDir, res.Location)
				if err != nil {
					rel = res.Location
				}
				fmt.Fprintf(&sb, "=== %s", rel)
				if res.Language != "" {
					fmt.Fprintf(&sb, " (%s)", res.Language)
				}
				fmt.Fprintln(&sb, " ===")
				for _, lr := range lines {
					fmt.Fprintf(&sb, "%4d: %s\n", lr.LineNumber, lr.Content)
				}
				sb.WriteByte('\n')
			}
			out := strings.TrimRight(sb.String(), "\n")
			if out == "" {
				return "(no matches)", nil
			}
			return out, nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "web_search",
			Description: openai.String("Search the web using DuckDuckGo. This is a fallback search tool — prefer any web search tool provided by MCP servers when available. Returns titles, URLs, and snippets for the top results."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"query":       map[string]any{"type": "string", "description": "Search query"},
					"max_results": map[string]any{"type": "number", "description": "Maximum number of results to return (default 10, max 10)"},
					"reasoning":   map[string]any{"type": "string", "description": "One short sentence explaining why this search is being performed"},
				},
				"required": []string{"query", "reasoning"},
			},
		}),
		execute: func(ctx context.Context, args map[string]any, _ string) (string, error) {
			query := str(args, "query")
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			max := ddgMaxResults
			if m, ok := args["max_results"].(float64); ok && m > 0 {
				max = int(m)
				if max > ddgMaxResults {
					max = ddgMaxResults
				}
			}
			results, err := duckduckgoSearch(ctx, query, max)
			if err != nil {
				return "", err
			}
			if len(results) == 0 {
				return "(no results)", nil
			}
			var sb strings.Builder
			for i, r := range results {
				fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, r.Title, r.URL)
				if r.Snippet != "" {
					fmt.Fprintf(&sb, "   %s\n", r.Snippet)
				}
				sb.WriteByte('\n')
			}
			return strings.TrimRight(sb.String(), "\n"), nil
		},
	},
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        "ask_user",
			Description: openai.String("Ask the user to pick from a list of options. Use this when you need a clear choice before proceeding. The question should be brief; send a normal message first if more context is needed."),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"question": map[string]any{"type": "string", "description": "A brief question to present to the user"},
					"options": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "2–6 choices to present. The user may also type a custom reply.",
						"minItems":    2,
						"maxItems":    6,
					},
				},
				"required": []string{"question", "options"},
			},
		}),
		// Handled specially in the agent loop; never reaches executeTool.
		execute: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			return "", fmt.Errorf("ask_user must be handled by the agent loop")
		},
	},
}

func schemas() []openai.ChatCompletionToolUnionParam {
	out := make([]openai.ChatCompletionToolUnionParam, len(builtinTools))
	for i, t := range builtinTools {
		out[i] = t.schema
	}
	return out
}

func executeTool(ctx context.Context, name, argsJSON, workDir string) (string, bool) {
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("invalid arguments: %v", err), true
	}
	for _, t := range builtinTools {
		fn := t.schema.GetFunction()
		if fn != nil && fn.Name == name {
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
