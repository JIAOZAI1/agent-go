// Package openai provides an executor for the OpenAI Chat Completions API.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/tool"
)

const defaultBaseURL = "https://api.openai.com/v1"

// Config configures an OpenAI executor.
type Config struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

// Executor executes requests against OpenAI Chat Completions.
type Executor struct {
	ref        model.Ref
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewExecutor creates an executor bound to ref.
func NewExecutor(ref model.Ref, config Config) (*Executor, error) {
	if strings.TrimSpace(ref.ProviderID) == "" {
		return nil, invalidRequest("provider ID is empty")
	}
	if strings.TrimSpace(ref.ModelID) == "" {
		return nil, invalidRequest("model ID is empty")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, invalidRequest("API key is empty")
	}

	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &Executor{
		ref:        ref,
		baseURL:    baseURL,
		apiKey:     config.APIKey,
		httpClient: httpClient,
	}, nil
}

// Model returns the model bound to the executor.
func (e *Executor) Model() model.Ref {
	return e.ref
}

// Generate sends one non-streaming Chat Completions request and exposes its
// result through the common model.Stream contract.
func (e *Executor) Generate(ctx context.Context, request model.Request) (model.Stream, error) {
	if ctx == nil {
		return nil, invalidRequest("context is nil")
	}
	if err := validateRequest(request); err != nil {
		return nil, err
	}

	body, err := json.Marshal(chatCompletionRequest{
		Model:       e.ref.ModelID,
		Messages:    toOpenAIMessages(request.Messages),
		Tools:       toOpenAITools(request.Tools),
		Temperature: request.Options.Temperature,
		MaxTokens:   request.Options.MaxTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAI request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("create OpenAI request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+e.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(httpRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &model.Error{Kind: model.ErrorUnavailable, Err: err}
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, statusError(response.StatusCode)
	}

	var completion chatCompletionResponse
	if err := json.NewDecoder(response.Body).Decode(&completion); err != nil {
		return nil, &model.Error{Kind: model.ErrorUnavailable, Err: fmt.Errorf("decode response: %w", err)}
	}
	if len(completion.Choices) == 0 {
		return nil, &model.Error{Kind: model.ErrorUnavailable, Err: errors.New("response contains no choices")}
	}

	return &stream{events: toStreamEvents(completion.Choices[0], completion.Usage)}, nil
}

type stream struct {
	events []model.Event
	index  int
	closed bool
}

func (s *stream) Recv(ctx context.Context) (model.Event, error) {
	if ctx == nil {
		return model.Event{}, invalidRequest("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return model.Event{}, err
	}
	if s.closed || s.index == len(s.events) {
		return model.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *stream) Close() error {
	s.closed = true
	return nil
}

// toStreamEvents converts one OpenAI choice into the model.Event sequence
// consumed by model.Collect (or a manual Recv loop): an optional text delta,
// one event per tool call, and a trailing event carrying usage and finish
// reason. Response assembly happens once in model.Collect; this function only
// translates OpenAI's wire format into the model.Event contract.
func toStreamEvents(choice chatCompletionChoice, usage openAIUsage) []model.Event {
	events := make([]model.Event, 0, 2+len(choice.Message.ToolCalls))
	if choice.Message.Content != "" {
		events = append(events, model.Event{Delta: choice.Message.Content})
	}
	for _, call := range choice.Message.ToolCalls {
		toolCall := message.ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: []byte(call.Function.Arguments),
		}
		events = append(events, model.Event{ToolCall: &toolCall})
	}
	events = append(events, model.Event{
		Usage:        toUsage(usage),
		FinishReason: toFinishReason(choice.FinishReason),
	})
	return events
}

type chatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Tools       []openAITool    `json:"tools,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    *string          `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatCompletionResponse struct {
	Choices []chatCompletionChoice `json:"choices"`
	Usage   openAIUsage            `json:"usage"`
}

type chatCompletionChoice struct {
	Message      chatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type chatCompletionMessage struct {
	Content   string           `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

func validateRequest(request model.Request) error {
	if len(request.Messages) == 0 {
		return invalidRequest("messages are empty")
	}
	if request.Options.Temperature != nil && (*request.Options.Temperature < 0 || *request.Options.Temperature > 2) {
		return invalidRequest("temperature must be between 0 and 2")
	}
	if request.Options.MaxTokens != nil && *request.Options.MaxTokens < 1 {
		return invalidRequest("max tokens must be positive")
	}
	for index, item := range request.Messages {
		if item.Role != message.RoleSystem && item.Role != message.RoleUser && item.Role != message.RoleAssistant && item.Role != message.RoleTool {
			return invalidRequest(fmt.Sprintf("message %d has unsupported role", index))
		}
		if item.Role == message.RoleTool && item.ToolCallID == "" {
			return invalidRequest(fmt.Sprintf("message %d has empty tool call ID", index))
		}
		for _, block := range item.Content {
			if block.Kind == message.ContentToolCall && item.Role == message.RoleAssistant {
				continue
			}
			if block.Kind != message.ContentText {
				return &model.Error{Kind: model.ErrorUnsupported, Err: fmt.Errorf("message %d has unsupported content block %q for role %q", index, block.Kind, item.Role)}
			}
		}
	}
	return nil
}

func toOpenAIMessages(values []message.Message) []openAIMessage {
	result := make([]openAIMessage, len(values))
	for index, item := range values {
		var text strings.Builder
		var calls []openAIToolCall
		for _, block := range item.Content {
			switch block.Kind {
			case message.ContentText:
				text.WriteString(block.Text)
			case message.ContentToolCall:
				if block.ToolCall != nil {
					calls = append(calls, toOpenAIToolCall(*block.ToolCall))
				}
			}
		}
		result[index] = openAIMessage{
			Role:       string(item.Role),
			Content:    contentPointer(text.String(), len(calls) > 0),
			ToolCalls:  calls,
			ToolCallID: item.ToolCallID,
		}
	}
	return result
}

// contentPointer omits the JSON content field only when the message has no
// text and carries tool calls instead, matching OpenAI's tolerance for a
// missing content field on tool-call-only assistant messages.
func contentPointer(text string, hasToolCalls bool) *string {
	if text == "" && hasToolCalls {
		return nil
	}
	return &text
}

func toOpenAIToolCall(value message.ToolCall) openAIToolCall {
	return openAIToolCall{
		ID:   value.ID,
		Type: "function",
		Function: openAIFunctionCall{
			Name:      value.Name,
			Arguments: string(value.Arguments),
		},
	}
}

func toOpenAITools(specs []tool.Spec) []openAITool {
	if len(specs) == 0 {
		return nil
	}
	result := make([]openAITool, len(specs))
	for index, spec := range specs {
		result[index] = openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  spec.Parameters,
			},
		}
	}
	return result
}

func toUsage(value openAIUsage) model.Usage {
	return model.Usage{InputTokens: value.PromptTokens, OutputTokens: value.CompletionTokens, TotalTokens: value.TotalTokens}
}

func toFinishReason(value string) model.FinishReason {
	switch value {
	case "stop":
		return model.FinishStop
	case "length":
		return model.FinishLength
	case "tool_calls", "function_call":
		return model.FinishToolCall
	default:
		return ""
	}
}

func invalidRequest(reason string) error {
	return &model.Error{Kind: model.ErrorInvalidRequest, Err: errors.New(reason)}
}

func statusError(statusCode int) error {
	kind := model.ErrorInvalidRequest
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = model.ErrorAuthentication
	case http.StatusTooManyRequests:
		kind = model.ErrorRateLimited
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		kind = model.ErrorUnavailable
	}
	return &model.Error{Kind: kind, Err: fmt.Errorf("OpenAI API returned HTTP %d", statusCode)}
}
