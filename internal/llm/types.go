package llm

// ChatMessage is one turn in the conversation sent to the API.
type ChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc holds the function name and arguments.
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded
}

// Tool defines a function the model can invoke.
type Tool struct {
	Type     string       `json:"type"` // "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the schema for a callable function.
type ToolFunction struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"` // JSON Schema object
}

// Request is the body for POST /chat/completions.
type Request struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []Tool        `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

// Response is what the client returns after a complete streaming exchange.
type Response struct {
	Content    string
	ToolCalls  []ToolCall
	StopReason string // "stop" | "tool_calls" | "length"
}

// streamChunk is one SSE data payload in the streaming response.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}
