package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/scope"
)

type executor struct{}

func (executor) Model() model.Ref                                              { return model.Ref{} }
func (executor) Generate(context.Context, model.Request) (model.Stream, error) { return nil, nil }

type strategy struct{ text string }

func (s *strategy) Run(_ context.Context, runScope *scope.Scope) (agent.Result, error) {
	runScope.RecordTurn()
	return agent.Result{Message: message.Text(message.RoleAssistant, s.text)}, nil
}

func TestDefaultStrategyFactoryFreezeAndSelect(t *testing.T) {
	factory := agent.NewDefaultStrategyFactory()
	registered := &strategy{text: "ok"}
	if err := factory.Register("default", registered); err != nil {
		t.Fatal(err)
	}
	if err := factory.Default("default"); err != nil {
		t.Fatal(err)
	}
	if err := factory.Freeze(); err != nil {
		t.Fatal(err)
	}
	if err := factory.Register("other", &strategy{}); !errors.Is(err, agent.ErrStrategyFactoryFrozen) {
		t.Fatalf("Register() error = %v", err)
	}
	got, err := factory.Select(context.Background(), agent.Request{})
	if err != nil || got != registered {
		t.Fatalf("Select() = %v, %v", got, err)
	}
}

func TestAgentRuntimeRun(t *testing.T) {
	factory := agent.NewDefaultStrategyFactory()
	if err := factory.Register("echo", &strategy{text: "done"}); err != nil {
		t.Fatal(err)
	}
	if err := factory.Default("echo"); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewRuntimeBuilder().Executor(executor{}).StrategyFactory(factory).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.Request{Input: message.Text(message.RoleUser, "go")})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Message.Content[0].Text != "done" || result.Stats.TurnCount != 1 {
		t.Fatalf("Result = %+v", result)
	}
}
