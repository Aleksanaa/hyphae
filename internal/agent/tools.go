package agent

import (
	"context"
	"fmt"
	"path/filepath"

	openai "github.com/openai/openai-go/v3"
)

type toolDef struct {
	schema  openai.ChatCompletionToolUnionParam
	execute func(ctx context.Context, args map[string]any, workDir string) (string, error)
}

// builtinTools contains the single tool exposed to the LLM.
// All individual operations are available as built-in functions inside run scripts.
var builtinTools = []toolDef{
	{
		schema: openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name: "run",
			Description: openai.String(`Execute a Starlark program. All operations are available as built-in functions; only print() output is returned — unlike a Python REPL, expression values are NOT shown automatically. Always use print() to see results.

Built-ins (first arg positional, rest keyword-only):
  read_file(path, offset=?, limit=?)
  write_file(path, content=)
  edit_file(path, edits=)           edits: list of {old_string, new_string} dicts
  list_directory(path=?)            returns list of filenames (dirs end with /)
  run_shell(command)
  web_fetch(url, format=?)          format: "markdown"|"text"|"html"
  web_search(query, max_results=?)  returns list of {title, url, snippet} dicts
  search_files(pattern, path=?, glob=?, case_sensitive=?)  returns list of {file, line, content} dicts
  ask_user(question, options=)

write_file, edit_file, run_shell, web_fetch, web_search require approval and pause for confirmation; denial raises an error.`),
			Parameters: openai.FunctionParameters{
				"type": "object",
				"properties": map[string]any{
					"code": map[string]any{"type": "string", "description": "Starlark program to execute"},
				},
				"required": []string{"code"},
			},
		}),
		// Handled specially in the agent loop; never reaches execute.
		execute: func(_ context.Context, _ map[string]any, _ string) (string, error) {
			return "", fmt.Errorf("run must be handled by the agent loop")
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
