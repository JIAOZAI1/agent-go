package prompt

import "context"

// Static renders one fixed system prompt.
type Static struct {
	text string
}

// NewStatic creates an immutable static Renderer.
func NewStatic(text string) *Static {
	return &Static{text: text}
}

// Render returns the configured text unchanged.
func (s *Static) Render(ctx context.Context, _ Input) (string, error) {
	if s == nil {
		return "", ErrNilRenderer
	}
	if ctx == nil {
		return "", ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.text, nil
}

var _ Renderer = (*Static)(nil)
