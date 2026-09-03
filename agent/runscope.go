package agent

import (
	"sync"
	"time"

	"github.com/JIAOZAI1/agent-go/model"
)

// RunMeta is the immutable identity of one run. It is fixed when the run
// starts and never changes during its lifetime.
type RunMeta struct {
	RunID       string    // stable id for the run
	ParentRunID string    // parent's RunID for nested/sub runs; empty when none
	SessionID   string    // conversation identifier (plain string, session-agnostic)
	CreatedAt   time.Time // lifecycle anchor
}

// RunStats is a snapshot of per-run counters and token usage. It is advanced
// during a run via RunScope methods; read it through RunScope.Stats (value
// copy) or, on completion, from RunResult.Stats.
//
// Semantics: Turn = one loop of the strategy; Step = one unit of work inside
// a turn (each model call or each tool call); the exact granularity of what a
// strategy treats as a step is decided by that strategy.
type RunStats struct {
	TurnCount     int
	StepCount     int
	ToolCallCount int
	InputTokens   int
	OutputTokens  int
	TotalTokens   int
	RetryCount    int
}

// RunScope carries the per-run identity and running statistics for one Run
// invocation. It is created at the start of a Run and is private to that Run:
// it must never be shared across goroutines, stashed in a context, or pooled.
// Concurrent safety within one Run (e.g. future controlled step parallelism)
// is provided by an internal mutex.
type RunScope struct {
	mu    sync.Mutex
	meta  RunMeta
	seq   uint64
	stats RunStats
}

// NewRunScope creates an empty scope for one run. The event sequence counter
// starts at 0 so the first NextSequence call returns 1.
func NewRunScope(meta RunMeta) *RunScope {
	return &RunScope{meta: meta}
}

// Meta returns a copy of the run's immutable identity.
func (s *RunScope) Meta() RunMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.meta
}

// NextSequence allocates the next monotonic sequence number, starting at 1,
// used for ordering events published within a single run.
func (s *RunScope) NextSequence() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// Stats returns a value snapshot of the accumulated counters and usage.
func (s *RunScope) Stats() RunStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// RecordTurn records the completion of one strategy loop (a turn).
func (s *RunScope) RecordTurn() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.TurnCount++
}

// RecordStep records the completion of one unit of work inside a turn. The
// granularity of a step is decided by the running strategy (e.g. a model call
// or a tool call).
func (s *RunScope) RecordStep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.StepCount++
}

// RecordToolCall records that one tool invocation was triggered.
func (s *RunScope) RecordToolCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.ToolCallCount++
}

// RecordUsage accumulates the token usage reported by one model call.
func (s *RunScope) RecordUsage(u model.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.InputTokens += u.InputTokens
	s.stats.OutputTokens += u.OutputTokens
	s.stats.TotalTokens += u.TotalTokens
}

// RecordRetry records one retry of a unit of work. It is reserved for
// strategies that implement retry; it stays unused (0) by strategies that do
// not retry.
func (s *RunScope) RecordRetry() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.RetryCount++
}
