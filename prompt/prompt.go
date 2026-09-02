// Package prompt defines provider-independent system-prompt renderers.
package prompt

import (
	"context"
	"errors"
)

// Values contains explicit string variables for one render.
type Values map[string]string

// Input is the per-render snapshot passed to a Renderer.
type Input struct {
	Values Values
}

// Renderer generates one complete system prompt.
type Renderer interface {
	Render(context.Context, Input) (string, error)
}

// RendererFunc adapts a function to Renderer.
type RendererFunc func(context.Context, Input) (string, error)

// Render invokes f after validating the context.
func (f RendererFunc) Render(ctx context.Context, input Input) (string, error) {
	if f == nil {
		return "", ErrNilRenderer
	}
	if ctx == nil {
		return "", ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	output, err := f(ctx, Input{Values: cloneValues(input.Values)})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	if err != nil {
		return "", err
	}
	return output, nil
}

var (
	// ErrInvalidContext indicates that Render received a nil context.
	ErrInvalidContext = errors.New("prompt: invalid context")
	// ErrNilRenderer indicates that a renderer receiver or function is nil.
	ErrNilRenderer = errors.New("prompt: nil renderer")
	// ErrInvalidTemplate indicates that a template cannot be constructed or used.
	ErrInvalidTemplate = errors.New("prompt: invalid template")
	// ErrRender indicates that template execution failed.
	ErrRender = errors.New("prompt: render")
)

func cloneValues(values Values) Values {
	if values == nil {
		return nil
	}
	cloned := make(Values, len(values))
	for name, value := range values {
		cloned[name] = value
	}
	return cloned
}
