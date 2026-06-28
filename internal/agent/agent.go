package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aleksana/hyphae/internal/llm"
	"github.com/aleksana/hyphae/internal/session"
)

const systemPrompt = `You are a skilled coding assistant. You help the user read, write, and reason about code.

You have tools to read files, edit files (targeted replacements), write files, list directories, run shell commands, fetch web pages, and search for text. Use them methodically: understand the task, explore when needed, make targeted changes, and verify your work.

For file edits, prefer edit_file over write_file — it takes an edits array of {old_string, new_string} pairs and applies them in order. Each old_string must appear exactly once in the file; include enough surrounding context to make it unique. Use write_file only to create new files.

run_shell, web_fetch, write_file, and edit_file require user approval before executing. Always fill in the "reasoning" field with one short sentence explaining why — it is shown to the user in the approval prompt.`

// EventType classifies an agent event sent to the UI.
type EventType string

const (
	EventTextDelta   EventType = "text_delta"   // partial assistant text
	EventToolStart   EventType = "tool_start"   // tool call beginning
	EventToolDone    EventType = "tool_done"    // tool call finished
	EventDone        EventType = "done"         // turn complete
	EventError       EventType = "error"        // unrecoverable error
	EventToolApproval EventType = "tool_approval" // waiting for user approval
)

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
	Text   string     // EventTextDelta
	Tool   *ToolEvent
	Err    error      // EventError
	RespCh chan ApprovalResult // EventToolApproval only; send exactly once
}

// Agent orchestrates the LLM ↔ tool loop.
type Agent struct {
	client *llm.Client
}

// New creates an Agent using the provided LLM client.
func New(client *llm.Client) *Agent {
	return &Agent{client: client}
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

		// Build the LLM request from current history
		msgs := buildMessages(sess)

		// Start streaming assistant response
		assistantMsg := session.Message{Role: session.RoleAssistant, Partial: true}
		msgIdx := sess.AddMessage(assistantMsg)

		resp, err := a.client.Complete(ctx, msgs, tools, func(delta string) {
			sess.AppendTextDelta(msgIdx, delta)
			select {
			case ch <- Event{Type: EventTextDelta, Text: delta}:
			case <-ctx.Done():
			}
		})
		if err != nil {
			sess.SetMessageError(msgIdx, err)
			select {
			case ch <- Event{Type: EventError, Err: err}:
			default:
			}
			return
		}

		// Finalize assistant message
		sess.FinalizeMessage(msgIdx, resp.Content, toSessionToolUses(resp.ToolCalls))

		if len(resp.ToolCalls) == 0 {
			// No tools — turn is complete
			ch <- Event{Type: EventDone}
			return
		}

		// Execute tools
		for _, tc := range resp.ToolCalls {
			if ctx.Err() != nil {
				return
			}

			te := &ToolEvent{CallID: tc.ID, Name: tc.Function.Name, Input: tc.Function.Arguments}

			// Some tools require user approval before running.
			if requiresApproval(tc.Function.Name) {
				te.Reasoning, te.Input = extractReasoning(tc.Function.Arguments)
				te.FilePath, te.DiffPatch = computeDiffForApproval(tc.Function.Name, tc.Function.Arguments, sess.WorkDir)
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
						Role: session.RoleTool,
						ToolResult: &session.ToolResult{ID: tc.ID, Content: denied, IsError: true},
					})
					sess.SetToolResult(msgIdx, tc.ID, denied, true)
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

			// Record result in session
			sess.AddMessage(session.Message{
				Role: session.RoleTool,
				ToolResult: &session.ToolResult{
					ID:      tc.ID,
					Content: output,
					IsError: isErr,
				},
			})
			// Update the tool use status in the assistant message
			sess.SetToolResult(msgIdx, tc.ID, output, isErr)

			select {
			case ch <- Event{Type: EventToolDone, Tool: te}:
			case <-ctx.Done():
				return
			}
		}

		// Loop back to call LLM again with tool results
	}
}

// buildMessages converts the session history to LLM chat messages.
func buildMessages(sess *session.Session) []llm.ChatMessage {
	msgs := []llm.ChatMessage{{Role: "system", Content: systemPrompt}}

	history, _ := sess.Snapshot()
	for _, m := range history {
		if m.Partial {
			continue // skip incomplete messages
		}
		switch m.Role {
		case session.RoleUser:
			msgs = append(msgs, llm.ChatMessage{Role: "user", Content: m.Content})

		case session.RoleAssistant:
			msg := llm.ChatMessage{Role: "assistant", Content: m.Content}
			for _, tu := range m.ToolUses {
				msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
					ID:   tu.ID,
					Type: "function",
					Function: llm.ToolCallFunc{
						Name:      tu.Name,
						Arguments: tu.Input,
					},
				})
			}
			msgs = append(msgs, msg)

		case session.RoleTool:
			if m.ToolResult != nil {
				content := m.ToolResult.Content
				if m.ToolResult.IsError {
					content = fmt.Sprintf("[error] %s", content)
				}
				msgs = append(msgs, llm.ChatMessage{
					Role:       "tool",
					Content:    content,
					ToolCallID: m.ToolResult.ID,
				})
			}
		}
	}
	return msgs
}

// requiresApproval returns true for tools that need user confirmation before running.
func requiresApproval(name string) bool {
	switch name {
	case "run_shell", "web_fetch", "write_file", "edit_file":
		return true
	}
	return false
}

// extractReasoning pulls the "reasoning" field out of run_shell args JSON.
// Returns (reasoning, originalJSON). The original JSON is kept intact so
// executeTool can still parse the "command" field normally.
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

func toSessionToolUses(tcs []llm.ToolCall) []session.ToolUse {
	out := make([]session.ToolUse, len(tcs))
	for i, tc := range tcs {
		out[i] = session.ToolUse{ID: tc.ID, Name: tc.Function.Name, Input: tc.Function.Arguments}
	}
	return out
}
