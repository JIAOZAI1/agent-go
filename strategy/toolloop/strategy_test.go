package toolloop_test

import (
	"context"
	"io"
	"testing"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/strategy/toolloop"
)

type executor struct{}

func (executor) Model() model.Ref { return model.Ref{ProviderID: "test", ModelID: "test"} }
func (executor) Generate(context.Context, model.Request) (model.Stream, error) {
	return &stream{events: []model.Event{{Delta: "hello"}, {FinishReason: model.FinishStop}}}, nil
}

type stream struct {
	events []model.Event
	index  int
}

func (s *stream) Recv(context.Context) (model.Event, error) {
	if s.index == len(s.events) {
		return model.Event{}, io.EOF
	}
	value := s.events[s.index]
	s.index++
	return value, nil
}
func (s *stream) Close() error { return nil }

func TestStrategyThroughRuntime(t *testing.T) {
	strategy, err := toolloop.New(toolloop.Options{})
	if err != nil {
		t.Fatal(err)
	}
	factory := agent.NewDefaultStrategyFactory()
	if err := factory.Register("tool-loop", strategy); err != nil {
		t.Fatal(err)
	}
	if err := factory.Default("tool-loop"); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntimeBuilder().Executor(executor{}).StrategyFactory(factory).Build()
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.Run(context.Background(), agent.Request{Input: message.Text(message.RoleUser, "hi")})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Message.Content[0].Text != "hello" || result.Stats.TurnCount != 1 || result.Stats.StepCount != 1 {
		t.Fatalf("Result = %+v", result)
	}
}
