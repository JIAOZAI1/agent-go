package prompt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JIAOZAI1/agent-go/prompt"
)

func TestStaticAndTemplate(t *testing.T) {
	static := prompt.NewStatic("fixed")
	if got, err := static.Render(context.Background(), prompt.Input{}); err != nil || got != "fixed" {
		t.Fatalf("Static.Render() = %q, %v", got, err)
	}
	renderer, err := prompt.NewTemplate(prompt.TemplateConfig{
		Name: "assistant", Text: "{{.role}}/{{.language}}", Defaults: prompt.Values{"language": "zh"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderer.Render(context.Background(), prompt.Input{Values: prompt.Values{"role": "support"}})
	if err != nil || got != "support/zh" {
		t.Fatalf("Template.Render() = %q, %v", got, err)
	}
}

func TestTemplateMissingValue(t *testing.T) {
	renderer, err := prompt.NewTemplate(prompt.TemplateConfig{Name: "missing", Text: "{{.missing}}"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderer.Render(context.Background(), prompt.Input{})
	if !errors.Is(err, prompt.ErrRender) || got != "" {
		t.Fatalf("Render() = %q, %v, want ErrRender and empty output", got, err)
	}
}

func TestRendererValidation(t *testing.T) {
	var renderer prompt.RendererFunc
	if _, err := renderer.Render(context.Background(), prompt.Input{}); !errors.Is(err, prompt.ErrNilRenderer) {
		t.Fatalf("nil renderer error = %v", err)
	}
	if _, err := prompt.NewStatic("x").Render(nil, prompt.Input{}); !errors.Is(err, prompt.ErrInvalidContext) { //nolint:staticcheck // deliberate nil-context negative test
		t.Fatalf("nil context error = %v", err)
	}
}
