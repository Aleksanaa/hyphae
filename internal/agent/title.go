package agent

import (
	"context"
	_ "embed"
	"regexp"
	"strings"

	openai "github.com/openai/openai-go/v3"

	"github.com/aleksanaa/hyphae/internal/strutil"
)

//go:embed title.md
var titlePrompt string

// titleThinkTags strips <think>…</think> reasoning blocks some models prepend.
var titleThinkTags = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// maxTitleLen caps the generated title; longer output is truncated with an ellipsis.
const maxTitleLen = 60

// GenerateTitle asks the model for a short thread title summarizing the given
// conversation transcript. It runs a single non-streaming completion with a
// dedicated prompt and no tools — the same client/model the session already
// uses. Returns "" if the model gives nothing usable (callers keep their
// placeholder title on empty/error).
func (a *Agent) GenerateTitle(ctx context.Context, conversation string) (string, error) {
	resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: a.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(titlePrompt),
			openai.UserMessage("Generate a title for this conversation:\n" + conversation),
		},
	})
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", nil
	}
	return cleanTitle(resp.Choices[0].Message.Content), nil
}

// cleanTitle strips reasoning tags, takes the first non-empty line, and clamps
// the length to maxTitleLen runes.
func cleanTitle(raw string) string {
	raw = titleThinkTags.ReplaceAllString(raw, "")
	var line string
	for l := range strings.SplitSeq(raw, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	if line == "" {
		return ""
	}
	return strutil.Truncate(line, maxTitleLen)
}
