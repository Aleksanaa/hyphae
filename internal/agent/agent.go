package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/aleksanaa/hyphae/internal/session"
)

const systemPrompt = `You are a skilled coding assistant. You help the user read, write, and reason about code.

You have tools to read files, edit files (targeted replacements), write files, list directories, run shell commands, fetch web pages, and search for text. Use them methodically: understand the task, explore when needed, make targeted changes, and verify your work.

For file edits, prefer edit_file over write_file — it takes an edits array of {old_string, new_string} pairs and applies them in order. Each old_string must appear exactly once in the file; include enough surrounding context to make it unique. Use write_file only to create new files.

run_shell, web_fetch, write_file, and edit_file require user approval before executing. Always fill in the "reasoning" field with one short sentence explaining why — it is shown to the user in the approval prompt.`

// EventType classifies an agent event sent to the UI.
type EventType string

const (
	EventTextDelta      EventType = "text_delta"      // partial assistant text
	EventReasoningDelta EventType = "reasoning_delta" // partial reasoning/thinking content
	EventToolStart      EventType = "tool_start"      // tool call beginning
	EventToolDone       EventType = "tool_done"       // tool call finished
	EventDone           EventType = "done"            // turn complete
	EventError          EventType = "error"           // unrecoverable error
	EventToolApproval   EventType = "tool_approval"   // waiting for user approval
	EventConnecting     EventType = "connecting"      // establishing connection (may be retrying)
	EventPreparingTool  EventType = "preparing_tool"  // tool calls received, about to execute
)

const maxConnectAttempts = 3

// ApprovalResult is the user's response to an approval request.
type ApprovalResult struct {
	Allowed    bool
	DenyReason string
}

// ToolEvent carries info about a single tool invocation.
type ToolEvent struct {
	CallID    string
	Name      string
	Input     string // raw JSON args (reasoning stripped for display)
	Reasoning string
	Output    string // filled in on EventToolDone
	IsError   bool
	// Set for write_file and edit_file before approval.
	FilePath  string // relative path of file being changed
	DiffPatch string // unified diff of the pending change
}

// Event is one item from the agent event stream.
type Event struct {
	Type   EventType
	Text   string // EventTextDelta
	Tool   *ToolEvent
	Err    error               // EventError
	RespCh chan ApprovalResult // EventToolApproval only; send exactly once
	// EventConnecting fields:
	Attempt     int           // current attempt number (1-based)
	MaxAttempts int           // total allowed attempts
	RetryAfter  time.Duration // >0 means waiting before the next attempt
}

// Agent orchestrates the LLM ↔ tool loop.
type Agent struct {
	client openai.Client
	model  string
}

// New creates an Agent using the provided OpenAI client and model name.
func New(baseURL, apiKey, model string) *Agent {
	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(apiKey),
	)
	return &Agent{client: client, model: model}
}

// ListModels returns the model IDs available at the configured base URL.
func (a *Agent) ListModels(ctx context.Context) ([]string, error) {
	page, err := a.client.Models.List(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(page.Data))
	for i, m := range page.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

// Send starts the agent loop from the current session state.
// The caller must have already added the user message to the session.
// Returns a channel of events; closed when the turn is complete or errored.
func (a *Agent) Send(ctx context.Context, sess *session.Session) <-chan Event {
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		a.loop(ctx, sess, ch)
	}()
	return ch
}

func (a *Agent) loop(ctx context.Context, sess *session.Session, ch chan<- Event) {
	tools := schemas()

	for {
		if ctx.Err() != nil {
			return
		}

		msgs := buildMessages(sess)

		sess.AddMessage(session.Message{Role: session.RoleStatus})
		assistantMsg := session.Message{Role: session.RoleAssistant, Partial: true}
		msgIdx := sess.AddMessage(assistantMsg)

		var acc openai.ChatCompletionAccumulator
		var streamErr error
		for attempt := 1; attempt <= maxConnectAttempts; attempt++ {
			select {
			case ch <- Event{Type: EventConnecting, Attempt: attempt, MaxAttempts: maxConnectAttempts}:
			case <-ctx.Done():
				return
			}

			stream := a.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
				Model:    a.model,
				Messages: msgs,
				Tools:    tools,
			})

			acc = openai.ChatCompletionAccumulator{}
			for stream.Next() {
				chunk := stream.Current()
				acc.AddChunk(chunk)
				if len(chunk.Choices) > 0 {
					d := chunk.Choices[0].Delta
					// Emit reasoning before content so the UI freeze order is correct.
					var raw struct {
						ReasoningContent string `json:"reasoning_content"`
					}
					if err := json.Unmarshal([]byte(d.RawJSON()), &raw); err == nil && raw.ReasoningContent != "" {
						sess.AppendThinkingDelta(msgIdx, raw.ReasoningContent)
						select {
						case ch <- Event{Type: EventReasoningDelta, Text: raw.ReasoningContent}:
						case <-ctx.Done():
						}
					}
					if d.Content != "" {
						sess.AppendTextDelta(msgIdx, d.Content)
						select {
						case ch <- Event{Type: EventTextDelta, Text: d.Content}:
						case <-ctx.Done():
						}
					}
				}
			}
			streamErr = stream.Err()

			// Stop retrying on success, context cancellation, or if a partial
			// response was already received (mid-stream failures are not resumable).
			if streamErr == nil || !isRetryable(streamErr) || len(acc.Choices) > 0 {
				break
			}
			if attempt < maxConnectAttempts {
				delay := time.Duration(1<<attempt) * time.Second // 2s, 4s
				select {
				case ch <- Event{Type: EventConnecting, Attempt: attempt, MaxAttempts: maxConnectAttempts, RetryAfter: delay, Err: streamErr}:
				case <-ctx.Done():
					return
				}
				select {
				case <-time.After(delay):
				case <-ctx.Done():
					return
				}
			}
		}

		if streamErr != nil {
			sess.SetMessageError(msgIdx, streamErr)
			select {
			case ch <- Event{Type: EventError, Err: streamErr}:
			default:
			}
			return
		}

		var toolCalls []openai.ChatCompletionMessageToolCallUnion
		content := ""
		if len(acc.Choices) > 0 {
			content = acc.Choices[0].Message.Content
			toolCalls = acc.Choices[0].Message.ToolCalls
		}

		sess.FinalizeMessage(msgIdx, content, toSessionToolUses(toolCalls))

		if len(toolCalls) == 0 {
			ch <- Event{Type: EventDone}
			return
		}

		select {
		case ch <- Event{Type: EventPreparingTool}:
		case <-ctx.Done():
			return
		}

		for _, tc := range toolCalls {
			if ctx.Err() != nil {
				return
			}

			te := &ToolEvent{CallID: tc.ID, Name: tc.Function.Name, Input: tc.Function.Arguments}

			if requiresApproval(tc.Function.Name) {
				te.Reasoning, te.Input = extractReasoning(tc.Function.Arguments)
				var diffErr error
				te.FilePath, te.DiffPatch, diffErr = computeDiffForApproval(tc.Function.Name, tc.Function.Arguments, sess.WorkDir)
				if diffErr != nil {
					errMsg := diffErr.Error()
					sess.AddMessage(session.Message{
						Role:       session.RoleTool,
						ToolResult: &session.ToolResult{ID: tc.ID, Content: errMsg, IsError: true},
					})
					sess.SetToolResult(msgIdx, tc.ID, errMsg, true)
					te.Output = errMsg
					te.IsError = true
					select {
					case ch <- Event{Type: EventToolDone, Tool: te}:
					case <-ctx.Done():
					}
					continue
				}
				sess.SetToolState(msgIdx, tc.ID, "pending")
				respCh := make(chan ApprovalResult, 1)
				select {
				case ch <- Event{Type: EventToolApproval, Tool: te, RespCh: respCh}:
				case <-ctx.Done():
					return
				}
				var approval ApprovalResult
				select {
				case approval = <-respCh:
				case <-ctx.Done():
					return
				}
				if !approval.Allowed {
					denied := "Execution denied by user."
					if approval.DenyReason != "" {
						denied += " Reason: " + approval.DenyReason
					}
					sess.AddMessage(session.Message{
						Role:       session.RoleTool,
						ToolResult: &session.ToolResult{ID: tc.ID, Content: denied, IsError: true},
					})
					sess.SetToolResult(msgIdx, tc.ID, denied, true)
					sess.SetToolState(msgIdx, tc.ID, "refused")
					te.Output = denied
					te.IsError = true
					select {
					case ch <- Event{Type: EventToolDone, Tool: te}:
					case <-ctx.Done():
					}
					continue
				}
			}

			if requiresApproval(tc.Function.Name) {
				sess.SetToolState(msgIdx, tc.ID, "running")
			}

			select {
			case ch <- Event{Type: EventToolStart, Tool: te}:
			case <-ctx.Done():
				return
			}

			output, isErr := executeTool(ctx, tc.Function.Name, tc.Function.Arguments, sess.WorkDir)
			te.Output = output
			te.IsError = isErr

			sess.AddMessage(session.Message{
				Role: session.RoleTool,
				ToolResult: &session.ToolResult{
					ID:      tc.ID,
					Content: output,
					IsError: isErr,
				},
			})
			sess.SetToolResult(msgIdx, tc.ID, output, isErr)

			select {
			case ch <- Event{Type: EventToolDone, Tool: te}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func buildMessages(sess *session.Session) []openai.ChatCompletionMessageParamUnion {
	msgs := []openai.ChatCompletionMessageParamUnion{openai.SystemMessage(systemPrompt)}

	history, _ := sess.Snapshot()
	for _, m := range history {
		if m.Partial || m.Error != nil {
			continue
		}
		switch m.Role {
		case session.RoleStatus:
			// UI-only; never sent to the model

		case session.RoleUser:
			msgs = append(msgs, openai.UserMessage(m.Content))

		case session.RoleAssistant:
			var p openai.ChatCompletionAssistantMessageParam
			if m.Content != "" {
				p.Content.OfString = openai.String(m.Content)
			}
			for _, tu := range m.ToolUses {
				p.ToolCalls = append(p.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: tu.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      tu.Name,
							Arguments: tu.Input,
						},
					},
				})
			}
			// Skip assistant turns with neither content nor tool calls — some providers
			// (e.g. deepseek) only populate CoT and leave content empty; sending an
			// empty assistant message causes an "invalid request" error.
			if m.Content == "" && len(m.ToolUses) == 0 {
				continue
			}
			msgs = append(msgs, openai.ChatCompletionMessageParamUnion{OfAssistant: &p})

		case session.RoleTool:
			if m.ToolResult != nil {
				content := m.ToolResult.Content
				if m.ToolResult.IsError {
					content = fmt.Sprintf("[error] %s", content)
				}
				msgs = append(msgs, openai.ToolMessage(content, m.ToolResult.ID))
			}
		}
	}
	return msgs
}

func requiresApproval(name string) bool {
	switch name {
	case "run_shell", "web_fetch", "write_file", "edit_file":
		return true
	}
	return false
}

func extractReasoning(argsJSON string) (reasoning, input string) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", argsJSON
	}
	if r, ok := args["reasoning"]; ok {
		json.Unmarshal(r, &reasoning)
	}
	return reasoning, argsJSON
}

// isRetryable returns true for transient errors that warrant a retry.
// Context cancellation and deadline are not retried.
func isRetryable(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func toSessionToolUses(tcs []openai.ChatCompletionMessageToolCallUnion) []session.ToolUse {
	out := make([]session.ToolUse, len(tcs))
	for i, tc := range tcs {
		key, params := parseToolDisplay(tc.Function.Name, tc.Function.Arguments)
		out[i] = session.ToolUse{
			ID: tc.ID, Name: tc.Function.Name, Input: tc.Function.Arguments,
			DisplayKey: key, DisplayParams: params,
		}
	}
	return out
}

// parseToolDisplay extracts a summary key and ordered display params from tool JSON input.
func parseToolDisplay(name, input string) (displayKey string, params []session.ToolParam) {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(input), &m) != nil {
		return "", nil
	}

	fmtVal := func(raw json.RawMessage) string {
		if len(raw) > 0 && raw[0] == '[' {
			var arr []json.RawMessage
			json.Unmarshal(raw, &arr)
			return fmt.Sprintf("[%d items]", len(arr))
		}
		if len(raw) > 0 && raw[0] == '{' {
			return "{...}"
		}
		var s string
		if json.Unmarshal(raw, &s) == nil {
			if runes := []rune(s); len(runes) > 200 {
				return string(runes[:197]) + "…"
			}
			return s
		}
		return string(raw)
	}

	primary := "path"
	switch name {
	case "run_shell":
		primary = "command"
	case "web_fetch":
		primary = "url"
	case "search_files":
		primary = "pattern"
	}
	if v, ok := m[primary]; ok {
		val := fmtVal(v)
		if runes := []rune(val); len(runes) > 50 {
			displayKey = string(runes[:47]) + "…"
		} else {
			displayKey = val
		}
		params = append(params, session.ToolParam{Key: primary, Value: val})
		delete(m, primary)
	}
	for _, k := range []string{"offset", "limit", "format", "timeout", "edits", "content"} {
		if v, ok := m[k]; ok {
			params = append(params, session.ToolParam{Key: k, Value: fmtVal(v)})
			delete(m, k)
		}
	}
	reasoning, hasReasoning := m["reasoning"]
	delete(m, "reasoning")
	for k, v := range m {
		params = append(params, session.ToolParam{Key: k, Value: fmtVal(v)})
	}
	if hasReasoning {
		params = append(params, session.ToolParam{Key: "reasoning", Value: fmtVal(reasoning)})
	}
	return
}
