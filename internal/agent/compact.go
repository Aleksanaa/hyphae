package agent

import (
	"context"
	_ "embed"
	"fmt"

	openai "github.com/openai/openai-go/v3"

	"github.com/aleksanaa/hyphae/internal/session"
)

//go:embed compact.md
var compactPrompt string

// appendHistory appends messages from history[startIdx:] to msgs, skipping
// status, partial, and errored messages. Returns whether any user message was added.
func appendHistory(msgs []openai.ChatCompletionMessageParamUnion, history []session.Message, startIdx int) ([]openai.ChatCompletionMessageParamUnion, bool) {
	hasContent := false
	for i, m := range history {
		if i < startIdx {
			continue
		}
		if m.Partial || m.Error != nil {
			continue
		}
		switch m.Role {
		case session.RoleStatus:
			// UI-only; skip
		case session.RoleUser:
			content := m.Content
			if m.SentLabel != "" {
				content += "\n\n" + m.SentLabel
			}
			msgs = append(msgs, openai.UserMessage(content))
			hasContent = true
		case session.RoleAssistant:
			// Skip turns with neither content nor tool calls — some providers
			// (e.g. deepseek) only populate CoT and leave content empty; sending
			// an empty assistant message causes an "invalid request" error.
			if m.Content == "" && len(m.ToolUses) == 0 {
				continue
			}
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
	var msgs []openai.ChatCompletionMessageParamUnion

	history, _ := sess.Snapshot()
	priorSummary, compactSeqs := sess.GetCompact()

	startIdx := 0
	if len(compactSeqs) > 0 {
		// Include the prior summary as context, then only append new messages.
		msgs = append(msgs, openai.UserMessage("[Prior conversation summary]\n\n"+priorSummary))
		var ap openai.ChatCompletionAssistantMessageParam
		ap.Content.OfString = openai.String("Understood, I have the prior context.")
		msgs = append(msgs, openai.ChatCompletionMessageParamUnion{OfAssistant: &ap})
		startIdx = compactSeqs[len(compactSeqs)-1] + 1
	}

	var hasContent bool
	msgs, hasContent = appendHistory(msgs, history, startIdx)
	if !hasContent {
		return "", CompactUsage{}, fmt.Errorf("nothing to compact")
	}
	// Compact prompt goes last so the history forms a stable cacheable prefix.
	// No system message — the instruction is self-contained in the prompt.
	msgs = append(msgs, openai.UserMessage(compactPrompt))

	resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    a.model,
		Messages: msgs,
	})
	if err != nil {
		return "", CompactUsage{}, err
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", CompactUsage{}, fmt.Errorf("empty response from model")
	}
	usage := CompactUsage{
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}
	return resp.Choices[0].Message.Content, usage, nil
}
