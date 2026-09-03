// Package scope provides the immutable capabilities and mutable per-run state
// shared by runtime strategies.
package scope

import (
	"sync"
	"time"

	"github.com/JIAOZAI1/agent-go/event"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
	"github.com/JIAOZAI1/agent-go/trim"
)

// Env is the immutable capability set available to every strategy.
type Env struct {
	Executor  model.Executor
	Tools     tool.Service
	Session   session.Store
	Prompt    prompt.Renderer
	EventSink event.Sink
	Trimmer   trim.Trimmer
}

// Input is the strategy-neutral input snapshot for one run.
type Input struct {
	SessionKey   session.Key
	PromptValues prompt.Values
	Message      message.Message
}

// Meta is the immutable identity of one run.
type Meta struct {
	RunID       string
	ParentRunID string
	SessionID   string
	CreatedAt   time.Time
}

// Stats is a snapshot of per-run counters and token usage.
type Stats struct {
	TurnCount, StepCount, ToolCallCount    int
	InputTokens, OutputTokens, TotalTokens int
	RetryCount                             int
}

// Scope carries capabilities, input, identity, and state for one run.
type Scope struct {
	env   Env
	input Input
	meta  Meta
	mu    sync.Mutex
	seq   uint64
	stats Stats
}

// Env returns the capability set by value.
func (s *Scope) Env() Env { return s.env }

// Input returns an independent input snapshot.
func (s *Scope) Input() Input { return cloneInput(s.input) }

// Meta returns the run identity.
func (s *Scope) Meta() Meta { return s.meta }

// NextSequence allocates a sequence number starting at one.
func (s *Scope) NextSequence() uint64 { s.mu.Lock(); defer s.mu.Unlock(); s.seq++; return s.seq }

// Stats returns a statistics snapshot.
func (s *Scope) Stats() Stats { s.mu.Lock(); defer s.mu.Unlock(); return s.stats }

// RecordTurn records one strategy turn.
func (s *Scope) RecordTurn() { s.mu.Lock(); defer s.mu.Unlock(); s.stats.TurnCount++ }

// RecordStep records one strategy step.
func (s *Scope) RecordStep() { s.mu.Lock(); defer s.mu.Unlock(); s.stats.StepCount++ }

// RecordToolCall records one tool invocation.
func (s *Scope) RecordToolCall() { s.mu.Lock(); defer s.mu.Unlock(); s.stats.ToolCallCount++ }

// RecordUsage accumulates model token usage.
func (s *Scope) RecordUsage(u model.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.InputTokens += u.InputTokens
	s.stats.OutputTokens += u.OutputTokens
	s.stats.TotalTokens += u.TotalTokens
}

// RecordRetry records one retry.
func (s *Scope) RecordRetry() { s.mu.Lock(); defer s.mu.Unlock(); s.stats.RetryCount++ }

func cloneInput(in Input) Input {
	in.Message = message.Clone(in.Message)
	if in.PromptValues != nil {
		values := make(prompt.Values, len(in.PromptValues))
		for key, value := range in.PromptValues {
			values[key] = value
		}
		in.PromptValues = values
	}
	return in
}
