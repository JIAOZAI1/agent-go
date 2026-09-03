package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JIAOZAI1/agent-go/event"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/scope"
	"github.com/JIAOZAI1/agent-go/session"
)

var (
	// ErrInvalidContext indicates a nil context.
	ErrInvalidContext = errors.New("agent: invalid context")
	// ErrInvalidRequest indicates an invalid run request.
	ErrInvalidRequest = errors.New("agent: invalid request")
	// ErrNilStrategyFactory indicates a missing strategy factory.
	ErrNilStrategyFactory = errors.New("agent: nil strategy factory")
	// ErrNilStrategy indicates that a factory returned or registered a nil strategy.
	ErrNilStrategy = errors.New("agent: nil strategy")
)

// Request is the public input contract of AgentRuntime.Run.
type Request struct {
	Strategy     string
	SessionKey   session.Key
	PromptValues prompt.Values
	Input        message.Message
}

// Result is the public output contract of AgentRuntime.Run.
type Result struct {
	Message  message.Message
	Revision session.Revision
	Stats    scope.Stats
}

// Strategy implements one reusable, concurrency-safe run policy.
type Strategy interface {
	Run(context.Context, *scope.Scope) (Result, error)
}

// AgentRuntime is an immutable, concurrency-safe strategy runtime.
type AgentRuntime struct {
	env     scope.Env
	factory StrategyFactory
}

// Run builds an isolated scope, selects a strategy, and executes it.
func (r *AgentRuntime) Run(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		return Result{}, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if r == nil || r.factory == nil {
		return Result{}, ErrNilStrategyFactory
	}

	runScope, err := scope.NewBuilder().Env(r.env).Input(scope.Input{
		SessionKey: request.SessionKey, PromptValues: request.PromptValues, Message: request.Input,
	}).Build()
	if err != nil {
		return Result{}, fmt.Errorf("agent: build run scope: %w", err)
	}

	publish(ctx, runScope, event.EventRunStarted, event.RunStarted{Input: request.Input})
	publish(ctx, runScope, event.EventInputReceived, event.InputReceived{Message: request.Input})

	strategy, err := r.factory.Select(ctx, request)
	if err != nil {
		return fail(ctx, runScope, err)
	}
	if strategy == nil {
		return fail(ctx, runScope, ErrNilStrategy)
	}

	result, err := strategy.Run(ctx, runScope)
	if err != nil {
		return fail(ctx, runScope, err)
	}
	result.Stats = runScope.Stats()
	publish(ctx, runScope, event.EventRunCompleted, event.RunCompleted{Message: result.Message})
	return result, nil
}

func fail(ctx context.Context, runScope *scope.Scope, err error) (Result, error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		publish(ctx, runScope, event.EventRunCancelled, event.RunCancelled{})
	} else {
		publish(ctx, runScope, event.EventRunFailed, event.RunFailed{ErrorKind: "run_failed"})
	}
	return Result{}, err
}

func publish(ctx context.Context, runScope *scope.Scope, eventType event.EventType, data any) {
	sink := runScope.Env().EventSink
	if sink == nil {
		return
	}
	_ = sink.Publish(ctx, event.Event{RunID: runScope.Meta().RunID, Sequence: runScope.NextSequence(), OccurredAt: time.Now(), Type: eventType, Data: data})
}
