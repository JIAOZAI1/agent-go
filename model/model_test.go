package model

import "testing"

func TestModelFields(t *testing.T) {
	m := Model{
		ID:            "model-id",
		Name:          "Model",
		ContextWindow: 128000,
		MaxTokens:     4096,
		Input:         []InputModality{InputText, InputImage},
		Reasoning:     true,
	}

	if m.ID != "model-id" || m.Name != "Model" {
		t.Fatalf("Model identity = (%q, %q), want (%q, %q)", m.ID, m.Name, "model-id", "Model")
	}
	if m.ContextWindow != 128000 || m.MaxTokens != 4096 {
		t.Fatalf("Model limits = (%d, %d), want (%d, %d)", m.ContextWindow, m.MaxTokens, 128000, 4096)
	}
	if len(m.Input) != 2 || !m.Reasoning {
		t.Fatalf("Model capabilities = (%v, %t), want two inputs and reasoning enabled", m.Input, m.Reasoning)
	}
}
