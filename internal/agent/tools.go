package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
			Description: openai.String(`Execute a Starlark program. All operations are available as built-in functions.

Built-ins (first arg positional, rest keyword-only):
  read_file(path, offset=?, limit=?) → string
  write_file(path, content=) → None
  edit_file(path, edits=) → None   edits: list of {old_string, new_string} dicts
  list_directory(path=?) → list of strings (dirs end with /)
  run_shell(command) → string (combined stdout+stderr)
  web_fetch(url, format=?) → string   format: "markdown"|"text"|"html"
  web_search(query, max_results=?) → list of {title, url, snippet} dicts
  search_files(path_glob, content_regex=?, exclude_glob=?, case_sensitive=?) → list of dicts. path_glob picks files by path ("**" spans directories; a bare glob like "*.go" matches the base name at any depth); use an absolute, ~, or working-dir-relative path to search other directories (subject to your read permissions). exclude_glob drops files whose path (or base name, for a bare glob) matches it, e.g. "**/testdata/**" or "*_test.go". With content_regex each matching line is returned as {file, line, content}; without it each matched file is returned as {file}.
  ask_user(question, options=) → string (selected option or free text)
  request_access(target, type=, reasoning=) → string   type: "readonly"|"readwrite"|"web_fetch"

Permissions & reasoning — every call is checked against what you are currently allowed to do:
  - Allowed → runs immediately; do NOT pass reasoning=. This covers read_file/list_directory/search_files within the working directory or the skills directory, write_file/edit_file within a readwrite grant, and web_fetch under a granted URL prefix.
  - Not allowed → pauses for the user to approve, and you MUST pass reasoning= saying why (omitting it errors; a denial also errors). This covers reading outside those places, any other write, every run_shell, web_fetch to an ungranted URL, and web_search.
Pass reasoning= only when a call is outside your permissions; when it is already allowed, omit it.

Paths may be absolute, relative to the working directory, or start with ~ for your home directory.

request_access(type, target, reasoning=) grants standing permission so repeated access to one place stops prompting (approved once, then silent). Grants are prefix-based on "/" boundaries: a directory grant covers it and everything under it; type="web_fetch" target="https://github.com/nixos" covers every URL under https://github.com/nixos/. Pick the type deliberately:
  - "readonly": request freely whenever you expect to read the same out-of-scope location more than once (e.g. the source of a cached dependency you keep consulting).
  - "web_fetch": request when you have a concrete reason to automate several fetches under one URL prefix (e.g. paging through docs on a single site); not for a one-off fetch.
  - "readwrite": request ONLY when the user has explicitly said they are handing full control of a directory or project over to you. Never on your own initiative.

Starlark limitations (sandboxed Python subset):
  no import       — modules can't be loaded; math/time/json are pre-loaded globals
  no class        — use dicts or plain functions instead
  no try/except   — errors abort the script; validate inputs before calling
  no yield        — generators are not supported; use lists and comprehensions
  no global/nonlocal — top-level variables are mutable globals by default`),
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

// resolvePath turns a path argument into an absolute path: "~"/"~/…" expand to
// the home directory (hpath), absolute paths pass through, and relative paths
// (rpath) resolve against the working directory.
func resolvePath(p, workDir string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return expandHome(p)
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(workDir, p)
}

func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}
