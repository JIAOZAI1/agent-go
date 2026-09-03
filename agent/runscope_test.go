package agent_test

import (
	"testing"
	"time"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/model"
)

func TestRunScopeMetaAndSequence(t *testing.T) {
	created := time.Now()
	meta := agent.RunMeta{RunID: "run-1", ParentRunID: "", SessionID: "s/1", CreatedAt: created}
	scope := agent.NewRunScope(meta)

	got := scope.Meta()
	if got != meta {
		t.Fatalf("Meta() = %+v, want %+v", got, meta)
	}
	if s := scope.NextSequence(); s != 1 {
		t.Fatalf("first NextSequence() = %d, want 1", s)
	}
	if s := scope.NextSequence(); s != 2 {
		t.Fatalf("second NextSequence() = %d, want 2", s)
	}
}

func TestRunScopeStatsAccumulation(t *testing.T) {
	scope := agent.NewRunScope(agent.RunMeta{RunID: "run-1"})

	scope.RecordTurn()
	scope.RecordStep()     // model call
	scope.RecordToolCall() //
	scope.RecordStep()     // tool call
	scope.RecordUsage(model.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15})
	scope.RecordUsage(model.Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5})

	stats := scope.Stats()
	if stats.TurnCount != 1 {
		t.Errorf("TurnCount = %d, want 1", stats.TurnCount)
	}
	if stats.StepCount != 2 {
		t.Errorf("StepCount = %d, want 2", stats.StepCount)
	}
	if stats.ToolCallCount != 1 {
		t.Errorf("ToolCallCount = %d, want 1", stats.ToolCallCount)
	}
	if stats.InputTokens != 13 || stats.OutputTokens != 7 || stats.TotalTokens != 20 {
		t.Errorf("usage = %+v, want in/out/total 13/7/20", stats)
	}
	if stats.RetryCount != 0 {
		t.Errorf("RetryCount = %d, want 0", stats.RetryCount)
	}
}

func TestRunScopeSnapshotIsolation(t *testing.T) {
	scope := agent.NewRunScope(agent.RunMeta{RunID: "run-1"})
	scope.RecordTurn()
	before := scope.Stats()
	scope.RecordTurn()
	after := scope.Stats()

	if before.TurnCount != 1 || after.TurnCount != 2 {
		t.Fatalf("snapshot isolation broken: before=%d after=%d", before.TurnCount, after.TurnCount)
	}
}
