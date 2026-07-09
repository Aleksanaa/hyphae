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
			Description: openai.String(`Execute a Starlark program with all operations available as built-in functions. Use this for any task that requires reading, writing, searching, or running commands — especially when it involves loops, conditionals, or chaining results.

Available built-in functions. The first argument may be passed positionally; remaining arguments are keyword-only:
  read_file(path, offset=?, limit=?)
  write_file(path, content=)
  edit_file(path, edits=)                     — edits is a list of {old_string, new_string} dicts
  list_directory(path=?)                      — returns a list of filename strings (dirs end with /)
  run_shell(command)
  web_fetch(url, format=?)                    — format: "markdown" (default)|"text"|"html"
  web_search(query, max_results=?)            — returns a list of {title, url, snippet} dicts
  search_files(pattern, path=?, glob=?, case_sensitive=?)        — returns a list of {file, line, content} dicts
  ask_user(question, options=)

IMPORTANT: built-in functions return their output as values. You must print() to see results:
  print(web_search("exchange rate"))
  content = read_file("main.go")
  print(content)

write_file, edit_file, run_shell, web_fetch, and web_search require user approval before executing and will pause the script for confirmation. If denied, the call raises an error and the script stops.

Two modules are available as globals — no import needed:
  math.sqrt(2), math.pi, math.log(x), math.sin(x), math.ceil(x), math.floor(x), ...
  time.now(), time.parse_duration("1h30m"), time.hour, time.minute, ...

The script's print() output is returned as the result.

Starlark is a sandboxed subset of Python. Supports: arithmetic, strings, lists, dicts, sets (set() built-in), list/dict comprehensions, for/while loops and if/else at any level, mutable global variables, recursive functions, and built-ins (len, range, int, float, str, sorted, min, max, zip, enumerate, type, ...). Does NOT support: import, class, try/except, yield, global/nonlocal, f-strings, ** exponentiation, or any stdlib beyond math and time.
For exponentiation use math.pow(x, y). The % operator supports basic specifiers (%s, %d, %f, %g) but NOT width or precision modifiers — "%.6f" % x will error. For rounded output use round(x, 6) which works like Python's two-argument round().`),
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
