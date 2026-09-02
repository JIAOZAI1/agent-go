package model

// Usage records token consumption when provided by the provider.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// FinishReason describes why generation ended.
type FinishReason string

const (
	FinishStop     FinishReason = "stop"
	FinishLength   FinishReason = "length"
	FinishToolCall FinishReason = "tool_call"
)
