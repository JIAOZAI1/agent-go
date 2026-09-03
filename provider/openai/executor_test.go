package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/tool"
)

func TestExecutorGenerate(t *testing.T) {
	type capturedRequest struct {
		Model    string `json:"model"`
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
		Temperature *float64 `json:"temperature"`
		MaxTokens   *int     `json:"max_tokens"`
	}

	server := newTestServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request = %s %s, authorization = %q", request.Method, request.URL.Path, request.Header.Get("Authorization"))
		}
		var got capturedRequest
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if got.Model != "gpt-test" || len(got.Messages) != 3 || got.Messages[1].Content != "hello " || got.Messages[2].Content != "world" {
			t.Errorf("request body = %+v", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"answer"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	temperature := 0.0
	maxTokens := 100
	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{BaseURL: server.URL + "/v1", APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	stream, err := executor.Generate(context.Background(), model.Request{
		Messages: []message.Message{message.Text(message.RoleSystem, "system"), message.Text(message.RoleUser, "hello "), message.Text(message.RoleUser, "world")},
		Options:  model.Options{Temperature: &temperature, MaxTokens: &maxTokens},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	response, err := model.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.Message.Content[0].Text != "answer" || response.Usage.TotalTokens != 5 || response.FinishReason != model.FinishStop {
		t.Fatalf("response = %+v, want answer and usage", response)
	}
}

func TestExecutorErrorsDoNotExposeAPIKey(t *testing.T) {
	server := newTestServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"secret"}}`))
	}))
	defer server.Close()

	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{BaseURL: server.URL, APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	_, err = executor.Generate(context.Background(), model.Request{Messages: []message.Message{message.Text(message.RoleUser, "hello")}})
	var modelErr *model.Error
	if err == nil || !errors.As(err, &modelErr) || modelErr.Kind != model.ErrorAuthentication || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Generate() error = %v, want authentication error without API key", err)
	}
}

type testServer struct {
	URL    string
	server *http.Server
	listen net.Listener
}

func newTestServer(handler http.Handler) *testServer {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	return &testServer{URL: "http://" + listener.Addr().String(), server: server, listen: listener}
}

func (s *testServer) Close() {
	_ = s.server.Close()
	_ = s.listen.Close()
}

func TestStreamContextAndClose(t *testing.T) {
	s := &stream{events: []model.Event{{Delta: "answer"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Recv(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Recv() error = %v, want context canceled", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	_, err = s.Recv(context.Background())
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Recv() after Close error = %v, want EOF", err)
	}
}

func TestExecutorGenerateSendsToolsAndToolCallHistory(t *testing.T) {
	type capturedMessage struct {
		Role      string `json:"role"`
		Content   *string
		ToolCalls []struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
	}
	type capturedRequest struct {
		Messages []capturedMessage `json:"messages"`
		Tools    []struct {
			Type     string `json:"type"`
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		} `json:"tools"`
	}

	var got capturedRequest
	server := newTestServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer server.Close()

	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{BaseURL: server.URL, APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	assistantWithCall := message.Message{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call-1", Name: "get_weather", Arguments: []byte(`{"city":"sf"}`)}},
		},
	}
	toolResult := message.Message{Role: message.RoleTool, ToolCallID: "call-1", Content: []message.ContentBlock{{Kind: message.ContentText, Text: "sunny"}}}

	_, err = executor.Generate(context.Background(), model.Request{
		Messages: []message.Message{message.Text(message.RoleUser, "weather?"), assistantWithCall, toolResult},
		Tools:    []tool.Spec{{Name: "get_weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	if len(got.Tools) != 1 || got.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("request tools = %+v, want get_weather", got.Tools)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("request messages = %+v, want 3 messages", got.Messages)
	}
	assistantMsg := got.Messages[1]
	if len(assistantMsg.ToolCalls) != 1 || assistantMsg.ToolCalls[0].ID != "call-1" || assistantMsg.ToolCalls[0].Function.Name != "get_weather" {
		t.Fatalf("assistant message tool_calls = %+v, want call-1/get_weather", assistantMsg.ToolCalls)
	}
	if assistantMsg.Content != nil {
		t.Errorf("assistant message content = %v, want omitted (nil) for tool-call-only message", *assistantMsg.Content)
	}
	toolMsg := got.Messages[2]
	if toolMsg.ToolCallID != "call-1" || toolMsg.Content == nil || *toolMsg.Content != "sunny" {
		t.Fatalf("tool message = %+v, want tool_call_id=call-1 content=sunny", toolMsg)
	}
}

func TestExecutorGenerateParsesToolCallResponse(t *testing.T) {
	server := newTestServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"sf\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{BaseURL: server.URL, APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	stream, err := executor.Generate(context.Background(), model.Request{Messages: []message.Message{message.Text(message.RoleUser, "weather?")}})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	response, err := model.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if response.FinishReason != model.FinishToolCall {
		t.Fatalf("FinishReason = %v, want %v", response.FinishReason, model.FinishToolCall)
	}
	calls := message.ToolCalls(response.Message)
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "get_weather" || string(calls[0].Arguments) != `{"city":"sf"}` {
		t.Fatalf("message.ToolCalls(response.Message) = %+v, want one get_weather call", calls)
	}
}

func TestValidateRequestAllowsAssistantToolCallBlocks(t *testing.T) {
	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	server := newTestServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{}}`))
	}))
	defer server.Close()
	executor.baseURL = server.URL

	assistantWithCall := message.Message{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call-1", Name: "f", Arguments: []byte(`{}`)}},
		},
	}
	_, err = executor.Generate(context.Background(), model.Request{Messages: []message.Message{message.Text(message.RoleUser, "hi"), assistantWithCall}})
	if err != nil {
		t.Fatalf("Generate() error = %v, want assistant ContentToolCall block to be accepted", err)
	}
}

func TestValidateRequestRejectsToolCallBlockOnNonAssistantRole(t *testing.T) {
	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	userWithCall := message.Message{
		Role: message.RoleUser,
		Content: []message.ContentBlock{
			{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "call-1", Name: "f", Arguments: []byte(`{}`)}},
		},
	}
	_, err = executor.Generate(context.Background(), model.Request{Messages: []message.Message{userWithCall}})
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != model.ErrorUnsupported {
		t.Fatalf("Generate() error = %v, want unsupported model error for tool call block on user message", err)
	}
}

func TestUnsupportedContent(t *testing.T) {
	executor, err := NewExecutor(model.Ref{ProviderID: "openai", ModelID: "gpt-test"}, Config{APIKey: "secret"})
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	_, err = executor.Generate(context.Background(), model.Request{Messages: []message.Message{{Role: message.RoleUser, Content: []message.ContentBlock{{Kind: message.ContentImage}}}}})
	var modelErr *model.Error
	if !errors.As(err, &modelErr) || modelErr.Kind != model.ErrorUnsupported {
		t.Fatalf("Generate() error = %v, want unsupported model error", err)
	}
}
