package agent

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/zendev-sh/goai"
	"github.com/zendev-sh/goai/provider"

	"github.com/aleksanaa/hyphae/internal/session"
)

//go:embed compact.md
var compactPrompt string

// appendHistory appends messages from history[startIdx:] to msgs, skipping
// thinking, partial, and errored items. Returns whether any user message was added.
//
// A model response's preamble text and its tool call are stored as separate peer
// items (RoleAssistant then RoleTool). Assistant text is buffered in pendingText:
// a following tool call reveals it was a preamble and drops it (an assistant
// message carrying both content and tool_calls is rejected by some providers);
// otherwise it flushes on its own as a final answer. The result therefore only
// ever contains assistant{tool_calls}, tool results, and standalone
// assistant{content} — never both together, and never two assistants in a row.
func appendHistory(msgs []provider.Message, history []session.Message, startIdx int) ([]provider.Message, bool) {
	hasContent := false
	pendingText := ""
	flushText := func() {
		if pendingText != "" {
			msgs = append(msgs, goai.AssistantMessage(pendingText))
			pendingText = ""
		}
	}
	for i, m := range history {
		if i < startIdx {
			continue
		}
		if m.Partial || m.Error != nil {
			continue
		}
		switch m.Role {
		case session.RoleThinking:
			// reasoning is not replayed to the model
		case session.RoleUser:
			flushText()
			content := m.Content
			if m.SentLabel != "" {
				content += "\n\n" + m.SentLabel
			}
			msgs = append(msgs, goai.UserMessage(content))
			hasContent = true
		case session.RoleAssistant:
			flushText()
			pendingText = m.Content
		case session.RoleTool:
			// Preamble text (if any) belongs to these calls; drop it rather than
			// emit an assistant message with both content and tool_calls.
			pendingText = ""
			for _, tu := range m.ToolUses {
				msgs = append(msgs, provider.Message{
					Role: provider.RoleAssistant,
					Content: []provider.Part{{
						Type:       provider.PartToolCall,
						ToolCallID: tu.ID,
						ToolName:   tu.Name,
						ToolInput:  []byte(tu.Input),
					}},
				})
				content := tu.Output
				if tu.State == "error" {
					content = fmt.Sprintf("[error] %s", content)
				}
				msgs = append(msgs, goai.ToolMessage(tu.ID, tu.Name, content))
			}
		}
	}
	flushText()
	return msgs, hasContent
}

// CompactUsage holds token counts returned by a Compact call.
type CompactUsage struct {
	PromptTokens     int64
	CompletionTokens int64
}

// Compact calls the model with the compact system prompt and produces a structured
// summary. When a prior compact exists, only the new messages since that compact
// are sent alongside the prior summary, avoiding redundant reprocessing.
// After success, call sess.SetCompact.
func (a *Agent) Compact(ctx context.Context, sess *session.Session) (string, CompactUsage, error) {
	var msgs []provider.Message

	history, _ := sess.Snapshot()
	priorSummary, compactSeqs := sess.GetCompact()

	startIdx := 0
	if len(compactSeqs) > 0 {
		// Include the prior summary as context, then only append new messages.
		msgs = append(msgs,
			goai.UserMessage("[Prior conversation summary]\n\n"+priorSummary),
			goai.AssistantMessage("Understood, I have the prior context."),
		)
		startIdx = compactSeqs[len(compactSeqs)-1] + 1
	}

	var hasContent bool
	msgs, hasContent = appendHistory(msgs, history, startIdx)
	if !hasContent {
		return "", CompactUsage{}, fmt.Errorf("nothing to compact")
	}
	// Compact prompt goes last so the history forms a stable cacheable prefix.
	// No system message — the instruction is self-contained in the prompt.
	msgs = append(msgs, goai.UserMessage(compactPrompt))

	resp, err := a.model.DoGenerate(ctx, provider.GenerateParams{Messages: msgs})
	if err != nil {
		return "", CompactUsage{}, err
	}
	if resp.Text == "" {
		return "", CompactUsage{}, fmt.Errorf("empty response from model")
	}
	usage := CompactUsage{
		PromptTokens:     int64(resp.Usage.InputTokens + resp.Usage.CacheReadTokens),
		CompletionTokens: int64(resp.Usage.OutputTokens),
	}
	return resp.Text, usage, nil
}
