package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	starlarkjson "github.com/aleksanaa/hyphae/internal/third_party/starlark/lib/json"
	starlarkmath "github.com/aleksanaa/hyphae/internal/third_party/starlark/lib/math"
	starlarktime "github.com/aleksanaa/hyphae/internal/third_party/starlark/lib/time"
	"github.com/aleksanaa/hyphae/internal/third_party/starlark/starlark"
	"github.com/aleksanaa/hyphae/internal/third_party/starlark/syntax"
	"github.com/boyter/gocodewalker"
)

// ── Starlark built-in implementations ────────────────────────────────────────

func starlarkReadFile(_ context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	path := resolvePath(str(args, "path"), workDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
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
		return starlark.String(fmt.Sprintf("(past end of file — file has %d lines)", total)), nil
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
	return starlark.String(sb.String()), nil
}

func starlarkWriteFile(_ context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	path := resolvePath(str(args, "path"), workDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	content := str(args, "content")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return starlark.String(fmt.Sprintf("wrote %d bytes to %s", len(content), path)), nil
}

func starlarkEditFile(_ context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	path := resolvePath(str(args, "path"), workDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	edits, _ := args["edits"].([]any)
	if len(edits) == 0 {
		return nil, fmt.Errorf("edits must be a non-empty array")
	}
	content := string(b)
	for i, e := range edits {
		em, ok := e.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edit %d: invalid format", i)
		}
		oldStr := str(em, "old_string")
		newStr := str(em, "new_string")
		count := strings.Count(content, oldStr)
		if count == 0 {
			return nil, fmt.Errorf("edit %d: old_string not found", i)
		}
		if count > 1 {
			return nil, fmt.Errorf("edit %d: old_string appears %d times — add more context to make it unique", i, count)
		}
		content = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return starlark.String(fmt.Sprintf("edited %s (%d replacements)", path, len(edits))), nil
}

func starlarkListDirectory(_ context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	p := str(args, "path")
	if p == "" {
		p = workDir
	} else {
		p = resolvePath(p, workDir)
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	elems := make([]starlark.Value, len(entries))
	for i, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		elems[i] = starlark.String(name)
	}
	return starlark.NewList(elems), nil
}

func starlarkRunShell(ctx context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", str(args, "command"))
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return starlark.String(string(out) + "\n[exit error: " + err.Error() + "]"), nil
	}
	return starlark.String(out), nil
}

func starlarkWebFetch(ctx context.Context, args map[string]any, _ string) (starlark.Value, error) {
	rawURL := str(args, "url")
	if rawURL == "" {
		return nil, fmt.Errorf("url is required")
	}
	format := str(args, "format")
	if format == "" {
		format = "markdown"
	}
	timeout := 0
	if t, ok := args["timeout"].(float64); ok {
		timeout = int(t)
	}
	s, err := fetchURL(ctx, rawURL, format, timeout)
	if err != nil {
		return nil, err
	}
	return starlark.String(s), nil
}

func starlarkWebSearch(ctx context.Context, args map[string]any, _ string) (starlark.Value, error) {
	query := str(args, "query")
	if query == "" {
		return nil, fmt.Errorf("query is required")
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
		return nil, err
	}
	elems := make([]starlark.Value, 0, len(results))
	for _, r := range results {
		d := new(starlark.Dict)
		d.SetKey(starlark.String("title"), starlark.String(r.Title))     //nolint:errcheck
		d.SetKey(starlark.String("url"), starlark.String(r.URL))         //nolint:errcheck
		d.SetKey(starlark.String("snippet"), starlark.String(r.Snippet)) //nolint:errcheck
		elems = append(elems, d)
	}
	return starlark.NewList(elems), nil
}

func starlarkSearchFiles(ctx context.Context, args map[string]any, workDir string) (starlark.Value, error) {
	pattern := str(args, "pattern")
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	p := str(args, "path")
	if p == "" {
		p = workDir
	} else {
		p = resolvePath(p, workDir)
	}

	glob := str(args, "glob")
	caseSensitive, _ := args["case_sensitive"].(bool)

	reStr := pattern
	if !caseSensitive {
		reStr = "(?i)" + pattern
	}
	re, err := regexp.Compile(reStr)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	searchCtx, cancelSearch := context.WithCancel(ctx)
	defer cancelSearch()

	fileQueue := make(chan *gocodewalker.File, 1000)
	walker := gocodewalker.NewParallelFileWalker([]string{p}, fileQueue)
	walker.ExcludeDirectory = []string{".git", ".hg", ".svn"}

	go func() {
		<-searchCtx.Done()
		walker.Terminate()
	}()
	go func() { _ = walker.Start() }()

	const maxResults = 500
	var elems []starlark.Value

	for f := range fileQueue {
		if searchCtx.Err() != nil {
			break
		}

		if glob != "" {
			if matched, _ := filepath.Match(glob, f.Filename); !matched {
				continue
			}
		}

		content, err := os.ReadFile(f.Location)
		if err != nil || len(content) == 0 {
			continue
		}

		// Skip binary files.
		check := content
		if len(check) > 10_000 {
			check = check[:10_000]
		}
		if bytes.IndexByte(check, 0) != -1 {
			continue
		}

		rel, err := filepath.Rel(workDir, f.Location)
		if err != nil {
			rel = f.Location
		}

		for lineNum, line := range strings.Split(string(content), "\n") {
			if re.MatchString(line) {
				d := new(starlark.Dict)
				d.SetKey(starlark.String("file"), starlark.String(rel))                              //nolint:errcheck
				d.SetKey(starlark.String("line"), starlark.MakeInt(lineNum+1))                       //nolint:errcheck
				d.SetKey(starlark.String("content"), starlark.String(strings.TrimRight(line, "\r"))) //nolint:errcheck
				elems = append(elems, d)
			}
		}

		if len(elems) >= maxResults {
			cancelSearch()
			break
		}
	}

	return starlark.NewList(elems), nil
}

// ── Script execution ──────────────────────────────────────────────────────────

// scriptTool pairs a Starlark built-in name with its Go implementation.
// firstParam is the parameter name that maps to the first positional argument,
// allowing natural call syntax like read_file("path") in addition to keyword form.
type scriptTool struct {
	name       string
	firstParam string
	fn         func(context.Context, map[string]any, string) (starlark.Value, error)
}

var scriptTools = []scriptTool{
	{"read_file", "path", starlarkReadFile},
	{"write_file", "path", starlarkWriteFile},
	{"edit_file", "path", starlarkEditFile},
	{"list_directory", "path", starlarkListDirectory},
	{"run_shell", "command", starlarkRunShell},
	{"web_fetch", "url", starlarkWebFetch},
	{"web_search", "query", starlarkWebSearch},
	{"search_files", "pattern", starlarkSearchFiles},
}

// runScript executes a Starlark program with all agent operations available as
// built-in functions. The script's print() output is returned as the result.
// Tools requiring user approval pause mid-script for confirmation.
func runScript(ctx context.Context, ch chan<- Event, argsJSON, workDir string) (string, bool) {
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Code == "" {
		return "code is required", true
	}

	var sb strings.Builder
	var counter atomic.Int64

	thread := &starlark.Thread{
		Print: func(_ *starlark.Thread, msg string) {
			sb.WriteString(msg)
			sb.WriteByte('\n')
		},
	}

	// Cancel the Starlark thread when the agent context expires.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel("cancelled")
		case <-done:
		}
	}()

	env := buildScriptEnv(ctx, ch, workDir, &counter)
	thread.SetMaxExecutionSteps(1_000_000_000)
	opts := &syntax.FileOptions{TopLevelControl: true, GlobalReassign: true, While: true, Set: true, Recursion: true, REPL: true}
	_, err := starlark.ExecFileOptions(opts, thread, "<run>", args.Code, env)
	if err != nil {
		out := sb.String()
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			out += evalErr.Backtrace()
		} else {
			out += err.Error()
		}
		return strings.TrimRight(out, "\n"), true
	}

	out := strings.TrimRight(sb.String(), "\n")
	if out == "" {
		return "(done)", false
	}
	return out, false
}

// buildScriptEnv builds the predeclared Starlark environment: math and time
// modules plus every operation wrapped as a built-in function.
func buildScriptEnv(ctx context.Context, ch chan<- Event, workDir string, counter *atomic.Int64) starlark.StringDict {
	env := starlark.StringDict{
		"json": starlarkjson.Module,
		"math": starlarkmath.Module,
		"time": starlarktime.Module,
		// divmod(a, b) returns (a//b, a%b) as a tuple, matching Python.
		"divmod": starlark.NewBuiltin("divmod", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if len(args) != 2 {
				return nil, fmt.Errorf("divmod: got %d arguments, want 2", len(args))
			}
			a, aok := starlark.AsFloat(args[0])
			b, bok := starlark.AsFloat(args[1])
			if !aok || !bok {
				return nil, fmt.Errorf("divmod: arguments must be numbers")
			}
			if b == 0 {
				return nil, fmt.Errorf("divmod: division by zero")
			}
			q := math.Trunc(a / b)
			r := a - q*b
			// Return ints when both inputs are integers.
			if _, aIsInt := args[0].(starlark.Int); aIsInt {
				if _, bIsInt := args[1].(starlark.Int); bIsInt {
					qi, _ := starlark.NumberToInt(starlark.Float(q))
					ri, _ := starlark.NumberToInt(starlark.Float(r))
					return starlark.Tuple{qi, ri}, nil
				}
			}
			return starlark.Tuple{starlark.Float(q), starlark.Float(r)}, nil
		}),
		// Shadow the built-in round() with a two-argument version: round(x, ndigits=0).
		"round": starlark.NewBuiltin("round", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			if len(args) < 1 || len(args) > 2 {
				return nil, fmt.Errorf("round: got %d arguments, want 1 or 2", len(args))
			}
			f, ok := starlark.AsFloat(args[0])
			if !ok {
				return nil, fmt.Errorf("round: first argument must be a number, got %s", args[0].Type())
			}
			if len(args) == 1 {
				// Match Python: round(x) returns int.
				return starlark.NumberToInt(starlark.Float(math.Round(f)))
			}
			n, ok := args[1].(starlark.Int)
			if !ok {
				return nil, fmt.Errorf("round: ndigits must be an integer, got %s", args[1].Type())
			}
			ndigits, _ := n.Int64()
			factor := math.Pow(10, float64(ndigits))
			return starlark.Float(math.Round(f*factor) / factor), nil
		}),
	}

	for _, t := range scriptTools {
		toolName := t.name
		toolFn := t.fn
		firstParam := t.firstParam
		env[toolName] = starlark.NewBuiltin(toolName, func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
			argsMap := kwargsToMap(kwargs)
			if len(args) > 0 && firstParam != "" {
				if _, exists := argsMap[firstParam]; !exists {
					argsMap[firstParam] = starlarkToGo(args[0])
				}
			}
			if requiresApproval(toolName) {
				jsonBytes, _ := json.Marshal(argsMap)
				argsJSON := string(jsonBytes)
				te := &ToolEvent{
					CallID: fmt.Sprintf("script:%d", counter.Add(1)),
					Name:   toolName,
				}
				te.Reasoning, te.Input = extractReasoning(argsJSON)
				var diffErr error
				te.FilePath, te.DiffPatch, diffErr = computeDiffForApproval(toolName, argsJSON, workDir)
				if diffErr != nil {
					return nil, diffErr
				}
				respCh := make(chan ApprovalResult, 1)
				select {
				case ch <- Event{Type: EventToolApproval, Tool: te, RespCh: respCh}:
				case <-ctx.Done():
					return nil, fmt.Errorf("cancelled")
				}
				var approval ApprovalResult
				select {
				case approval = <-respCh:
				case <-ctx.Done():
					return nil, fmt.Errorf("cancelled")
				}
				if !approval.Allowed {
					msg := "denied by user"
					if approval.DenyReason != "" {
						msg += ": " + approval.DenyReason
					}
					return nil, fmt.Errorf("%s", msg)
				}
			}
			return toolFn(ctx, argsMap, workDir)
		})
	}

	// ask_user: emits EventSelectPrompt and blocks until the user replies.
	env["ask_user"] = starlark.NewBuiltin("ask_user", func(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		m := kwargsToMap(kwargs)
		if len(args) > 0 {
			if _, exists := m["question"]; !exists {
				m["question"] = starlarkToGo(args[0])
			}
		}
		question, _ := m["question"].(string)
		te := &ToolEvent{
			CallID:         fmt.Sprintf("script:%d", counter.Add(1)),
			Name:           "ask_user",
			SelectQuestion: question,
			SelectOptions:  toStringSlice(m["options"]),
		}
		respCh := make(chan string, 1)
		select {
		case ch <- Event{Type: EventSelectPrompt, Tool: te, SelectRespCh: respCh}:
		case <-ctx.Done():
			return nil, fmt.Errorf("cancelled")
		}
		var answer string
		select {
		case answer = <-respCh:
		case <-ctx.Done():
			return nil, fmt.Errorf("cancelled")
		}
		return starlark.String(answer), nil
	})

	return env
}

// ── Argument conversion ───────────────────────────────────────────────────────

func kwargsToMap(kwargs []starlark.Tuple) map[string]any {
	m := make(map[string]any, len(kwargs))
	for _, kv := range kwargs {
		key, _ := starlark.AsString(kv[0])
		if key != "" {
			m[key] = starlarkToGo(kv[1])
		}
	}
	return m
}

func starlarkToGo(v starlark.Value) any {
	switch v := v.(type) {
	case starlark.String:
		return string(v)
	case starlark.Int:
		n, _ := v.Int64()
		return float64(n)
	case starlark.Float:
		return float64(v)
	case starlark.Bool:
		return bool(v)
	case *starlark.List:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = starlarkToGo(v.Index(i))
		}
		return out
	case *starlark.Dict:
		m := make(map[string]any, v.Len())
		for _, item := range v.Items() {
			k, _ := starlark.AsString(item[0])
			if k != "" {
				m[k] = starlarkToGo(item[1])
			}
		}
		return m
	}
	return v.String()
}

func toStringSlice(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
