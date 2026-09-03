package agent

import (
	"errors"
	"fmt"

	"github.com/JIAOZAI1/agent-go/event"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/scope"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
	"github.com/JIAOZAI1/agent-go/trim"
)

// RuntimeBuilder assembles an immutable AgentRuntime.
type RuntimeBuilder struct {
	env     scope.Env
	factory StrategyFactory
}

// NewRuntimeBuilder creates a Runtime builder.
func NewRuntimeBuilder() *RuntimeBuilder { return &RuntimeBuilder{} }
func (b *RuntimeBuilder) Executor(value model.Executor) *RuntimeBuilder {
	b.env.Executor = value
	return b
}
func (b *RuntimeBuilder) Tools(value tool.Service) *RuntimeBuilder { b.env.Tools = value; return b }
func (b *RuntimeBuilder) Session(value session.Store) *RuntimeBuilder {
	b.env.Session = value
	return b
}
func (b *RuntimeBuilder) Prompt(value prompt.Renderer) *RuntimeBuilder {
	b.env.Prompt = value
	return b
}
func (b *RuntimeBuilder) EventSink(value event.Sink) *RuntimeBuilder {
	b.env.EventSink = value
	return b
}
func (b *RuntimeBuilder) Trimmer(value trim.Trimmer) *RuntimeBuilder { b.env.Trimmer = value; return b }
func (b *RuntimeBuilder) StrategyFactory(value StrategyFactory) *RuntimeBuilder {
	b.factory = value
	return b
}

// Build validates and freezes dependencies before returning a Runtime.
func (b *RuntimeBuilder) Build() (*AgentRuntime, error) {
	if b == nil {
		return nil, errors.New("agent: invalid runtime builder")
	}
	if b.env.Executor == nil {
		return nil, scope.ErrNilExecutor
	}
	if b.factory == nil {
		return nil, ErrNilStrategyFactory
	}
	if err := b.factory.Freeze(); err != nil {
		return nil, fmt.Errorf("agent: freeze strategy factory: %w", err)
	}
	return &AgentRuntime{env: b.env, factory: b.factory}, nil
}
