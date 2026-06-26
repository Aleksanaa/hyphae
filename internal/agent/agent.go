package agent

import (
	"context"
	"fmt"

	"github.com/aleksana/hypane/internal/llm"
	"github.com/aleksana/hypane/internal/session"
)

const systemPrompt = `You are a skilled coding assistant. You help the user read, write, and reason about code.

You have tools to read and write files, list directories, run shell commands, and search for text. Use them methodically: understand the task, explore when needed, make targeted changes, and verify your work.

Be concise in explanations. Show code changes directly. When running commands, prefer short, focused ones.`

// EventType classifies an agent event sent to the UI.
type EventType string

const (
	EventTextDelta  EventType = "text_delta"  // partial assistant text
	EventToolStart  EventType = "tool_start"  // tool call beginning
	EventToolDone   EventType = "tool_done"   // tool call finished
	EventDone       EventType = "done"        // turn complete
	EventError      EventType = "error"       // unrecoverable error
)

// ToolEvent carries info about a single tool invocation.
type ToolEvent struct {
	CallID  string
	Name    string
	Input   string // raw JSON args
	Output  string // filled in on EventToolDone
	IsError bool
}

// Event is one item from the agent event stream.
type Event struct {
	Type      EventType
	Text      string    // EventTextDelta
	Tool      *ToolEvent
	Err       error     // EventError
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

func toSessionToolUses(tcs []llm.ToolCall) []session.ToolUse {
	out := make([]session.ToolUse, len(tcs))
	for i, tc := range tcs {
		out[i] = session.ToolUse{ID: tc.ID, Name: tc.Function.Name, Input: tc.Function.Arguments}
	}
	return out
}
