// Package singleturn implements a strategy that performs exactly one model
// generation without exposing or executing tools.
package singleturn

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/event"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/scope"
	"github.com/JIAOZAI1/agent-go/session"
)

const defaultTrimBudget = 8192

var (
	// ErrInvalidContext indicates that Run received a nil context.
	ErrInvalidContext = errors.New("singleturn: invalid context")
	// ErrInvalidScope indicates that Run received a nil Scope.
	ErrInvalidScope = errors.New("singleturn: invalid scope")
	// ErrUnexpectedToolCall indicates that the model returned a tool call even
	// though SingleTurn does not expose or execute tools.
	ErrUnexpectedToolCall = errors.New("singleturn: unexpected tool call")
)

// Options configures a reusable SingleTurn strategy.
type Options struct {
	ModelOptions model.Options
	TrimBudget   int
}

// Strategy performs one model generation per Run. It is immutable after
// construction and safe for concurrent use when the capabilities in Scope.Env
// are themselves safe for concurrent use.
type Strategy struct {
	modelOptions model.Options
	trimBudget   int
}

// New returns an immutable SingleTurn strategy.
func New(options Options) (*Strategy, error) {
	return &Strategy{
		modelOptions: cloneModelOptions(options.ModelOptions),
		trimBudget:   options.TrimBudget,
	}, nil
}

// Run renders the prompt, loads optional history, generates one response, and
// commits the current input and response to the optional session store.
func (s *Strategy) Run(ctx context.Context, runScope *scope.Scope) (agent.Result, error) {
	if ctx == nil {
		return agent.Result{}, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	if runScope == nil {
		return agent.Result{}, ErrInvalidScope
	}

	env := runScope.Env()
	input := runScope.Input()

	var systemText string
	if env.Prompt != nil {
		text, err := env.Prompt.Render(ctx, prompt.Input{Values: input.PromptValues})
		if err != nil {
			return agent.Result{}, fmt.Errorf("singleturn: render prompt: %w", err)
		}
		systemText = text
	}

	var history []message.Message
	var revision uint64
	if env.Session != nil {
		snapshot, err := env.Session.Load(ctx, input.SessionKey)
		if err != nil {
			return agent.Result{}, fmt.Errorf("singleturn: load session: %w", err)
		}
		history = snapshot.Messages
		revision = uint64(snapshot.Revision)
	}
	if env.Trimmer != nil && len(history) > 0 {
		history = env.Trimmer.Trim(history, resolveTrimBudget(s.trimBudget))
	}

	messages := make([]message.Message, 0, len(history)+2)
	if systemText != "" {
		messages = append(messages, message.SystemText(systemText))
	}
	messages = append(messages, message.CloneSlice(history)...)
	messages = append(messages, message.Clone(input.Message))

	runScope.RecordTurn()
	s.publish(ctx, runScope, event.EventModelGenerationStarted, event.ModelGenerationStarted{Model: env.Executor.Model()})
	stream, err := env.Executor.Generate(ctx, model.Request{
		Messages: messages,
		Options:  cloneModelOptions(s.modelOptions),
	})
	if err != nil {
		return agent.Result{}, fmt.Errorf("singleturn: generate response: %w", err)
	}
	runScope.RecordStep()

	response, err := model.Collect(ctx, s.observe(ctx, runScope, stream))
	if err != nil {
		return agent.Result{}, fmt.Errorf("singleturn: collect response: %w", err)
	}
	runScope.RecordUsage(response.Usage)
	s.publish(ctx, runScope, event.EventModelGenerationFinished, event.ModelGenerationFinished{
		Model:        env.Executor.Model(),
		Message:      response.Message,
		Usage:        response.Usage,
		FinishReason: response.FinishReason,
	})

	if len(message.ToolCalls(response.Message)) != 0 {
		return agent.Result{}, ErrUnexpectedToolCall
	}

	result := agent.Result{Message: response.Message, Stats: runScope.Stats()}
	if env.Session != nil {
		committed, err := env.Session.Append(ctx, input.SessionKey, sessionRevision(revision), []message.Message{
			message.Clone(input.Message),
			message.Clone(response.Message),
		})
		if err != nil {
			return agent.Result{}, fmt.Errorf("singleturn: commit session: %w", err)
		}
		result.Revision = committed
	}
	return result, nil
}

func (s *Strategy) publish(ctx context.Context, runScope *scope.Scope, eventType event.EventType, data any) {
	sink := runScope.Env().EventSink
	if sink == nil {
		return
	}
	_ = sink.Publish(ctx, event.Event{
		RunID:      runScope.Meta().RunID,
		Sequence:   runScope.NextSequence(),
		OccurredAt: time.Now(),
		Type:       eventType,
		Data:       data,
	})
}

func (s *Strategy) observe(ctx context.Context, runScope *scope.Scope, stream model.Stream) model.Stream {
	return observingStream{Stream: stream, onEvent: func(value model.Event) {
		if value.Delta == "" {
			return
		}
		s.publish(ctx, runScope, event.EventModelDelta, event.ModelDelta{
			Model:        runScope.Env().Executor.Model(),
			Delta:        value.Delta,
			FinishReason: value.FinishReason,
		})
	}}
}

type observingStream struct {
	model.Stream
	onEvent func(model.Event)
}

func (s observingStream) Recv(ctx context.Context) (model.Event, error) {
	value, err := s.Stream.Recv(ctx)
	if err != nil {
		return value, err
	}
	s.onEvent(value)
	return value, nil
}

func cloneModelOptions(options model.Options) model.Options {
	if options.Temperature != nil {
		value := *options.Temperature
		options.Temperature = &value
	}
	if options.MaxTokens != nil {
		value := *options.MaxTokens
		options.MaxTokens = &value
	}
	return options
}

func resolveTrimBudget(value int) int {
	if value > 0 {
		return value
	}
	return defaultTrimBudget
}

// sessionRevision keeps the session package type at the commit boundary while
// allowing the loaded revision to remain a plain scalar in the run state.
func sessionRevision(value uint64) session.Revision {
	return session.Revision(value)
}

var _ agent.Strategy = (*Strategy)(nil)
