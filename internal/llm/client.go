package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Client talks to an OpenAI-compatible chat completions endpoint.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
}

// New creates a Client.
func New(baseURL, apiKey, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Complete sends messages to the LLM and streams the response.
// onDelta is called for each text token as it arrives.
// Returns the complete response once the stream ends.
func (c *Client) Complete(ctx context.Context, msgs []ChatMessage, tools []Tool, onDelta func(string)) (*Response, error) {
	req := Request{
		Model:    c.model,
		Messages: msgs,
		Tools:    tools,
		Stream:   true,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody) //nolint
		return nil, fmt.Errorf("API error %d: %v", resp.StatusCode, errBody)
	}

	return c.readStream(resp, onDelta)
}

// ListModels returns the model IDs available at the configured base URL.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models endpoint: status %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	ids := make([]string, len(body.Data))
	for i, m := range body.Data {
		ids[i] = m.ID
	}
	return ids, nil
}

func (c *Client) readStream(resp *http.Response, onDelta func(string)) (*Response, error) {
	var (
		textBuf   strings.Builder
		stopReason string
		// accumulate tool calls by index across chunks
		toolMap = make(map[int]*ToolCall)
		toolIdx []int // insertion order
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		for _, choice := range chunk.Choices {
			if choice.FinishReason != "" {
				stopReason = choice.FinishReason
			}

			// Text delta
			if choice.Delta.Content != "" {
				textBuf.WriteString(choice.Delta.Content)
				if onDelta != nil {
					onDelta(choice.Delta.Content)
				}
			}

			// Tool call deltas — accumulate by index
			for _, tc := range choice.Delta.ToolCalls {
				idx := tc.Index
				if _, exists := toolMap[idx]; !exists {
					toolMap[idx] = &ToolCall{Type: "function"}
					toolIdx = append(toolIdx, idx)
				}
				t := toolMap[idx]
				if tc.ID != "" {
					t.ID = tc.ID
				}
				if tc.Function.Name != "" {
					t.Function.Name = tc.Function.Name
				}
				t.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Assemble tool calls in order
	toolCalls := make([]ToolCall, 0, len(toolIdx))
	for _, idx := range toolIdx {
		toolCalls = append(toolCalls, *toolMap[idx])
	}

	return &Response{
		Content:    textBuf.String(),
		ToolCalls:  toolCalls,
		StopReason: stopReason,
	}, nil
}
