package singleturn_test

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/strategy/singleturn"
)

type executor struct {
	mu      sync.Mutex
	request model.Request
	events  []model.Event
}

func (e *executor) Model() model.Ref { return model.Ref{ProviderID: "test", ModelID: "single"} }
func (e *executor) Generate(ctx context.Context, request model.Request) (model.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.request = request
	e.mu.Unlock()
	return &stream{events: e.events}, nil
}
func (e *executor) lastRequest() model.Request { e.mu.Lock(); defer e.mu.Unlock(); return e.request }

type stream struct {
	events []model.Event
	index  int
}

func (s *stream) Recv(ctx context.Context) (model.Event, error) {
	if err := ctx.Err(); err != nil {
		return model.Event{}, err
	}
	if s.index == len(s.events) {
		return model.Event{}, io.EOF
	}
	value := s.events[s.index]
	s.index++
	return value, nil
}
func (s *stream) Close() error { return nil }

func TestStrategyGeneratesOnceAndCommitsSession(t *testing.T) {
	temperature, maxTokens := 0.2, 128
	executor := &executor{events: []model.Event{
		{Delta: "hello"},
		{Usage: model.Usage{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}, FinishReason: model.FinishStop},
	}}
	store := session.NewMemoryStore()
	key := session.Key{Scope: "test", ID: "single"}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{message.UserText("previous")}); err != nil {
		t.Fatal(err)
	}

	strategy, err := singleturn.New(singleturn.Options{ModelOptions: model.Options{Temperature: &temperature, MaxTokens: &maxTokens}})
	if err != nil {
		t.Fatal(err)
	}
	factory := agent.NewDefaultStrategyFactory()
	if err := factory.Register("single", strategy); err != nil {
		t.Fatal(err)
	}
	if err := factory.Default("single"); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntimeBuilder().Executor(executor).Session(store).
		Prompt(prompt.NewStatic("system")).StrategyFactory(factory).Build()
	if err != nil {
		t.Fatal(err)
	}

	// Mutating constructor inputs must not alter the strategy's options.
	temperature, maxTokens = 0.9, 999
	result, err := runtime.Run(context.Background(), agent.Request{SessionKey: key, Input: message.UserText("current")})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Message.Content[0].Text != "hello" || result.Revision != 2 {
		t.Fatalf("Result = %+v", result)
	}
	if result.Stats.TurnCount != 1 || result.Stats.StepCount != 1 || result.Stats.ToolCallCount != 0 || result.Stats.TotalTokens != 3 {
		t.Fatalf("Stats = %+v", result.Stats)
	}

	request := executor.lastRequest()
	if len(request.Tools) != 0 {
		t.Fatalf("Tools = %+v, want none", request.Tools)
	}
	if len(request.Messages) != 3 || request.Messages[0].Role != message.RoleSystem || request.Messages[1].Content[0].Text != "previous" || request.Messages[2].Content[0].Text != "current" {
		t.Fatalf("Messages = %+v", request.Messages)
	}
	if *request.Options.Temperature != 0.2 || *request.Options.MaxTokens != 128 {
		t.Fatalf("Options = %+v", request.Options)
	}

	snapshot, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 3 || snapshot.Messages[1].Content[0].Text != "current" || snapshot.Messages[2].Content[0].Text != "hello" {
		t.Fatalf("Snapshot = %+v", snapshot)
	}
}

func TestStrategyRejectsUnexpectedToolCallWithoutCommit(t *testing.T) {
	call := message.ToolCall{ID: "call-1", Name: "unexpected", Arguments: []byte(`{}`)}
	executor := &executor{events: []model.Event{{ToolCall: &call}, {FinishReason: model.FinishToolCall}}}
	store := session.NewMemoryStore()
	strategy, _ := singleturn.New(singleturn.Options{})
	factory := agent.NewDefaultStrategyFactory()
	_ = factory.Register("single", strategy)
	_ = factory.Default("single")
	runtime, err := agent.NewRuntimeBuilder().Executor(executor).Session(store).StrategyFactory(factory).Build()
	if err != nil {
		t.Fatal(err)
	}
	key := session.Key{Scope: "test", ID: "tool-call"}
	_, err = runtime.Run(context.Background(), agent.Request{SessionKey: key, Input: message.UserText("go")})
	if !errors.Is(err, singleturn.ErrUnexpectedToolCall) {
		t.Fatalf("Run() error = %v", err)
	}
	snapshot, loadErr := store.Load(context.Background(), key)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(snapshot.Messages) != 0 {
		t.Fatalf("committed %d messages, want 0", len(snapshot.Messages))
	}
}
