package tool

import "context"

// Tool is an executable capability that can be composed into an agent.
type Tool interface {
	Name() string
	Description() string
	Execute(ctx context.Context, input []byte) ([]byte, error)
}
