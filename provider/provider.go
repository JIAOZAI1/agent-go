package provider

// Provider describes a service endpoint that exposes one or more AI models.
type Provider struct {
	ID      string
	Name    string
	BaseURL string
	API     APIType
	APIKey  string
}

// APIType identifies the protocol exposed by a provider.
type APIType string

const (
	APIOpenAICompletions APIType = "openai-completions"
	APIOpenAIResponses   APIType = "openai-responses"
	APIAnthropicMessages APIType = "anthropic-messages"
)
