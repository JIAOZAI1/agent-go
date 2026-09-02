package prompt

import (
	"bytes"
	"context"
	"fmt"
	"text/template"
)

// TemplateConfig configures one template compiled during setup.
type TemplateConfig struct {
	Name     string
	Text     string
	Defaults Values
}

// Template is an immutable strict text template.
type Template struct {
	parsed   *template.Template
	defaults Values
}

// NewTemplate parses config and snapshots its defaults.
func NewTemplate(config TemplateConfig) (*Template, error) {
	if config.Name == "" {
		return nil, fmt.Errorf("%w: empty name", ErrInvalidTemplate)
	}
	parsed, err := template.New(config.Name).
		Funcs(template.FuncMap{"index": strictIndex}).
		Option("missingkey=error").
		Parse(config.Text)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTemplate, err)
	}
	return &Template{parsed: parsed, defaults: cloneValues(config.Defaults)}, nil
}

// Render applies input values over defaults and executes the template.
func (t *Template) Render(ctx context.Context, input Input) (string, error) {
	if t == nil {
		return "", ErrNilRenderer
	}
	if t.parsed == nil {
		return "", fmt.Errorf("%w: uninitialized template", ErrInvalidTemplate)
	}
	if ctx == nil {
		return "", ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	values := cloneValues(t.defaults)
	if values == nil && input.Values != nil {
		values = make(Values, len(input.Values))
	}
	for name, value := range input.Values {
		values[name] = value
	}
	var output bytes.Buffer
	if err := t.parsed.Execute(&output, values); err != nil {
		return "", fmt.Errorf("%w: %w", ErrRender, err)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}
	return output.String(), nil
}

func strictIndex(values Values, name string) (string, error) {
	value, ok := values[name]
	if !ok {
		return "", fmt.Errorf("missing variable %q", name)
	}
	return value, nil
}

var _ Renderer = (*Template)(nil)
