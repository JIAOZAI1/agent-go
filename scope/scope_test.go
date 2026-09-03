package scope_test

import (
	"context"
	"errors"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/scope"
)

type executor struct{}

func (executor) Model() model.Ref                                              { return model.Ref{} }
func (executor) Generate(context.Context, model.Request) (model.Stream, error) { return nil, nil }

func TestBuilderValidationAndIsolation(t *testing.T) {
	if _, err := scope.NewBuilder().Build(); !errors.Is(err, scope.ErrNilExecutor) {
		t.Fatalf("Build() error = %v", err)
	}
	values := prompt.Values{"name": "before"}
	input := scope.Input{PromptValues: values, Message: message.Text(message.RoleUser, "hello")}
	runScope, err := scope.NewBuilder().Env(scope.Env{Executor: executor{}}).Input(input).Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	values["name"] = "after"
	if got := runScope.Input().PromptValues["name"]; got != "before" {
		t.Fatalf("PromptValues = %q, want before", got)
	}
	if runScope.Meta().RunID == "" || runScope.Meta().CreatedAt.IsZero() {
		t.Fatalf("Meta() = %+v, want generated identity", runScope.Meta())
	}
}

func TestScopeStatsAndSequence(t *testing.T) {
	runScope, _ := scope.NewBuilder().Env(scope.Env{Executor: executor{}}).Build()
	runScope.RecordTurn()
	runScope.RecordStep()
	runScope.RecordToolCall()
	runScope.RecordRetry()
	runScope.RecordUsage(model.Usage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5})
	if got := runScope.NextSequence(); got != 1 {
		t.Fatalf("NextSequence() = %d, want 1", got)
	}
	got := runScope.Stats()
	if got.TurnCount != 1 || got.StepCount != 1 || got.ToolCallCount != 1 || got.RetryCount != 1 || got.TotalTokens != 5 {
		t.Fatalf("Stats() = %+v", got)
	}
}
