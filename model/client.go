package model

import (
	"context"

	"github.com/JIAOZAI1/agent-go/message"
)

// Request contains the information required to generate a model response.
type Request struct {
	Messages []message.Message
	Options  Options
}

// Response contains the generated model output.
type Response struct {
	Message      message.Message
	Usage        Usage
	FinishReason FinishReason
}

// Options contains optional generation parameters.
type Options struct {
	Temperature *float64
	MaxTokens   *int
	Reasoning   bool
}

// Executor generates a streaming response for a bound model.
type Executor interface {
	Model() Ref
	Generate(ctx context.Context, request Request) (Stream, error)
}

// ExecutorFactory creates an executor bound to a specific model.
type ExecutorFactory interface {
	New(ctx context.Context, ref Ref) (Executor, error)
}
