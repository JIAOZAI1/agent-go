package model

// Model describes the capabilities and limits of an AI model.
type Model struct {
	ID            string
	Name          string
	ContextWindow int
	MaxTokens     int
	Input         []InputModality
	Reasoning     bool
}

// Ref identifies a model exposed by a provider.
type Ref struct {
	ProviderID string
	ModelID    string
}

// InputModality identifies an input type supported by a model.
type InputModality string

const (
	InputText  InputModality = "text"
	InputImage InputModality = "image"
	InputAudio InputModality = "audio"
)
