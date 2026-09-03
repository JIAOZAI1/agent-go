package toolloop

import (
	"context"
	"encoding/json"
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
	"github.com/JIAOZAI1/agent-go/tool"
	"github.com/JIAOZAI1/agent-go/trim"
)

const (
	defaultMaxTurns            = 8
	defaultMaxToolCallsPerTurn = 8
)

var (
	// ErrInvalidScope indicates that Run received a nil Scope.
	ErrInvalidScope = errors.New("toolloop: invalid scope")
	// ErrInvalidContext indicates that Run received a nil context.
	ErrInvalidContext = errors.New("agent: invalid context")
	// ErrMaxTurnsExceeded indicates that a run used more model calls than allowed.
	ErrMaxTurnsExceeded = errors.New("agent: max turns exceeded")
	// ErrTooManyToolCalls indicates that a model turn requested more tool calls than allowed.
	ErrTooManyToolCalls = errors.New("agent: too many tool calls in one turn")
	// ErrNoToolService indicates that the model requested a tool call but no tool.Service is configured.
	ErrNoToolService = errors.New("agent: model requested a tool call but no tool.Service is configured")
)

// Options configures a reusable ToolLoop strategy.
type Options struct {
	MaxTurns            int
	MaxToolCallsPerTurn int
	StopOnToolError     bool
	TrimBudget          int
}

// Strategy implements the model/tool loop using capabilities from Scope.Env.
type Strategy struct {
	maxTurns            int
	maxToolCallsPerTurn int
	stopOnToolError     bool
	trimBudget          int
}

// New returns an immutable, concurrency-safe ToolLoop strategy.
func New(options Options) (*Strategy, error) {
	maxTurns := options.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	maxCalls := options.MaxToolCallsPerTurn
	if maxCalls <= 0 {
		maxCalls = defaultMaxToolCallsPerTurn
	}
	return &Strategy{maxTurns: maxTurns, maxToolCallsPerTurn: maxCalls, stopOnToolError: options.StopOnToolError, trimBudget: options.TrimBudget}, nil
}

// Run executes one ToolLoop run. Safe for concurrent use.
func (a *Strategy) Run(ctx context.Context, runScope *scope.Scope) (agent.Result, error) {
	if ctx == nil {
		return agent.Result{}, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return agent.Result{}, err
	}
	if runScope == nil {
		return agent.Result{}, ErrInvalidScope
	}
	env, input := runScope.Env(), runScope.Input()

	var systemText string
	if env.Prompt != nil {
		text, err := env.Prompt.Render(ctx, prompt.Input{Values: input.PromptValues})
		if err != nil {
			return agent.Result{}, err
		}
		systemText = text
	}
	var snapshot session.Snapshot
	if env.Session != nil {
		loaded, err := env.Session.Load(ctx, input.SessionKey)
		if err != nil {
			return agent.Result{}, err
		}
		snapshot = loaded
	}
	if env.Trimmer != nil && env.Session != nil {
		snapshot.Messages = env.Trimmer.Trim(snapshot.Messages, trimBudget(env.Trimmer, a.trimBudget))
	}
	history := append(message.CloneSlice(snapshot.Messages), message.Clone(input.Message))

	for turn := 1; ; turn++ {
		if err := ctx.Err(); err != nil {
			return agent.Result{}, err
		}
		if turn > a.maxTurns {
			return agent.Result{}, ErrMaxTurnsExceeded
		}
		runScope.RecordTurn()
		a.publish(ctx, runScope, event.EventModelGenerationStarted, event.ModelGenerationStarted{Model: env.Executor.Model()})
		stream, err := env.Executor.Generate(ctx, model.Request{Messages: withSystemPrompt(systemText, history), Tools: toolSpecs(env.Tools)})
		if err != nil {
			return agent.Result{}, err
		}
		runScope.RecordStep()
		response, err := model.Collect(ctx, a.observe(ctx, runScope, stream))
		if err != nil {
			return agent.Result{}, err
		}
		runScope.RecordUsage(response.Usage)
		a.publish(ctx, runScope, event.EventModelGenerationFinished, event.ModelGenerationFinished{Model: env.Executor.Model(), Message: response.Message, Usage: response.Usage, FinishReason: response.FinishReason})
		history = append(history, response.Message)
		calls := message.ToolCalls(response.Message)
		if len(calls) == 0 {
			revision, err := commit(ctx, env.Session, input.SessionKey, snapshot.Revision, history[len(snapshot.Messages):])
			if err != nil {
				return agent.Result{}, err
			}
			return agent.Result{Message: response.Message, Revision: revision, Stats: runScope.Stats()}, nil
		}
		if len(calls) > a.maxToolCallsPerTurn {
			return agent.Result{}, ErrTooManyToolCalls
		}
		if env.Tools == nil {
			return agent.Result{}, ErrNoToolService
		}
		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return agent.Result{}, err
			}
			toolCall := tool.Call{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
			a.publish(ctx, runScope, event.EventToolCallRequested, event.ToolCallRequested{Call: toolCall})
			a.publish(ctx, runScope, event.EventToolExecutionStarted, event.ToolExecutionStarted{Call: toolCall})
			runScope.RecordToolCall()
			runScope.RecordStep()
			start := time.Now()
			result, err := env.Tools.Execute(ctx, toolCall)
			if err != nil {
				if a.stopOnToolError {
					return agent.Result{}, fmt.Errorf("toolloop: tool %q: %w", call.Name, err)
				}
				history = append(history, toolErrorMessage(call, err))
				continue
			}
			a.publish(ctx, runScope, event.EventToolExecutionFinished, event.ToolExecutionFinished{Call: toolCall, Result: result, Duration: time.Since(start)})
			history = append(history, toolResultMessage(call, result))
		}
	}
}

func (a *Strategy) publish(ctx context.Context, runScope *scope.Scope, eventType event.EventType, data any) {
	sink := runScope.Env().EventSink
	if sink == nil {
		return
	}
	_ = sink.Publish(ctx, event.Event{RunID: runScope.Meta().RunID, Sequence: runScope.NextSequence(), OccurredAt: time.Now(), Type: eventType, Data: data})
}

func (a *Strategy) observe(ctx context.Context, runScope *scope.Scope, stream model.Stream) model.Stream {
	return observingStream{Stream: stream, onEvent: func(value model.Event) {
		if value.Delta == "" {
			return
		}
		a.publish(ctx, runScope, event.EventModelDelta, event.ModelDelta{Model: runScope.Env().Executor.Model(), Delta: value.Delta, FinishReason: value.FinishReason})
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

func commit(ctx context.Context, store session.Store, key session.Key, expected session.Revision, batch []message.Message) (session.Revision, error) {
	if store == nil {
		return 0, nil
	}
	return store.Append(ctx, key, expected, batch)
}

func withSystemPrompt(systemText string, history []message.Message) []message.Message {
	if systemText == "" {
		return append([]message.Message(nil), history...)
	}
	messages := make([]message.Message, 0, len(history)+1)
	messages = append(messages, message.Text(message.RoleSystem, systemText))
	messages = append(messages, history...)
	return messages
}

func toolResultMessage(call message.ToolCall, result tool.Result) message.Message {
	msg := message.Text(message.RoleTool, result.Content)
	msg.ToolCallID = call.ID
	return msg
}

func toolErrorMessage(call message.ToolCall, err error) message.Message {
	msg := message.Text(message.RoleTool, err.Error())
	msg.ToolCallID = call.ID
	msg.IsError = true
	return msg
}

// trimHistoryDefaultBytes is the fallback working-set budget for the loaded
// session history when no explicit TrimBudget is set and a Trimmer is
// configured. It keeps roughly the last few KB of conversation so trimming
// stays deterministic out of the box. Deployments that need tighter control
// should pass an explicit TrimBudget derived from their model's context window.
const trimHistoryDefaultBytes = 8192

// trimBudget resolves the byte budget for trimming the loaded history. It
// returns 0 (disable) when no Trimmer is set. Otherwise it prefers an explicit
// TrimBudget and falls back to trimHistoryDefaultBytes so an explicitly
// configured Trimmer still trims even when the caller does not supply a budget.
func trimBudget(trimmer trim.Trimmer, explicit int) int {
	if trimmer == nil {
		return 0
	}
	if explicit > 0 {
		return explicit
	}
	return trimHistoryDefaultBytes
}

func toolSpecs(service tool.Service) []tool.Spec {
	if service == nil {
		return nil
	}
	return service.Specs()
}

var _ agent.Strategy = (*Strategy)(nil)
