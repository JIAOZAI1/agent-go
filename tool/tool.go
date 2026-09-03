// Package tool defines provider-independent tools and their execution runtime.
package tool

import (
	"context"
	"encoding/json"
)

// Spec describes a tool exposed to a model. Parameters is a JSON Schema object.
type Spec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Call is one model-requested tool invocation.
type Call struct {
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Result is the successful output of one tool invocation.
type Result struct {
	Content  string         `json:"content"`
	Metadata ResultMetadata `json:"metadata"`
}

// ResultMetadata describes runtime details of a tool invocation.
type ResultMetadata struct {
	CallID      string `json:"callId,omitempty"`
	ToolName    string `json:"toolName,omitempty"`
	StartTimeMS int64  `json:"startTimeMs"`
	EndTimeMS   int64  `json:"endTimeMs"`
	DurationMS  int64  `json:"durationMs"`
	Success     bool   `json:"success"`
}

// Tool implements one concrete capability. Implementations must support
// concurrent calls and honor context cancellation.
type Tool interface {
	Spec() Spec
	Execute(context.Context, json.RawMessage) (Result, error)
}

// ExecuteNext executes the remaining middleware and target Tool.
type ExecuteNext func(context.Context, Call) (Result, error)

// ExecuteMiddleware surrounds one ToolRuntime execution.
type ExecuteMiddleware func(context.Context, Call, ExecuteNext) (Result, error)

// Service is the minimal tool catalog and execution capability used by agents.
type Service interface {
	Specs() []Spec
	Execute(context.Context, Call) (Result, error)
}
