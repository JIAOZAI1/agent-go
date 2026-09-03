package agent

import (
	"context"
	"testing"

	"github.com/JIAOZAI1/agent-go/model"
)

// stubExecutor minimally satisfies model.Executor for white-box tests.
type stubExecutor struct{}

func (stubExecutor) Model() model.Ref { return model.Ref{ProviderID: "test", ModelID: "test"} }

func (stubExecutor) Generate(context.Context, model.Request) (model.Stream, error) {
	return nil, nil
}

func TestValidateRunEnv(t *testing.T) {
	tests := []struct {
		name string
		env  RunEnv
		want error
	}{
		{name: "nil executor", env: RunEnv{}, want: ErrNilExecutor},
		{name: "executor set", env: RunEnv{Executor: stubExecutor{}}, want: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validateRunEnv(test.env); got != test.want {
				t.Fatalf("validateRunEnv() = %v, want %v", got, test.want)
			}
		})
	}
}

// runRunner is satisfied by ToolLoopAgent; assert so the internal L3 run
// contract cannot drift silently.
func TestToolLoopAgentSatisfiesRunContract(t *testing.T) {
	var _ runRunner = (*ToolLoopAgent)(nil)
	_ = ToolLoopAgent{}
}
