package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
	"github.com/JIAOZAI1/agent-go/trim"
)

const (
	defaultMaxTurns            = 8
	defaultMaxToolCallsPerTurn = 8
)

var (
	// ErrNilExecutor indicates that a strategy was constructed with a nil
	// RunEnv.Executor.
	ErrNilExecutor = errors.New("agent: nil executor")
	// ErrInvalidContext indicates that Run received a nil context.
	ErrInvalidContext = errors.New("agent: invalid context")
	// ErrMaxTurnsExceeded indicates that a run used more model calls than allowed.
	ErrMaxTurnsExceeded = errors.New("agent: max turns exceeded")
	// ErrTooManyToolCalls indicates that a model turn requested more tool calls than allowed.
	ErrTooManyToolCalls = errors.New("agent: too many tool calls in one turn")
	// ErrNoToolService indicates that the model requested a tool call but no tool.Service is configured.
	ErrNoToolService = errors.New("agent: model requested a tool call but no tool.Service is configured")
)

// ToolLoopOptions configures a ToolLoopAgent. The capability fields mirror
// the canonical RunEnv set (see agent/run.go); they stay direct fields so
// existing flat composite literals continue to compile, because Go does not
// allow promoted embedded fields as struct-literal keys.
type ToolLoopOptions struct {
	Executor            model.Executor
	Tools               tool.Service
	Renderer            prompt.Renderer
	Store               session.Store
	EventSink           EventSink
	MaxTurns            int
	MaxToolCallsPerTurn int
	StopOnToolError     bool
	// Trimmer optionally bounds the loaded session history before it is sent
	// to the model each turn. nil disables trimming (send history untouched).
	Trimmer trim.Trimmer
	// TrimBudget caps the trimmed loaded history in bytes when > 0. When left at
	// 0, a default fallback budget is used so an explicit Trimmer still trims.
	// Callers should set this to a value derived from their model's context
	// window to match a specific deployment.
	TrimBudget int
}

// ToolLoopAgent orchestrates a model generation / tool execution loop: it
// calls Executor, executes any requested tool calls through Tools, feeds the
// results back to Executor, and repeats until the model stops requesting
// tools or a limit is reached.
//
// A ToolLoopAgent is immutable after construction and safe for concurrent
// use across goroutines: all per-run state lives in local variables inside
// Run.
type ToolLoopAgent struct {
	executor            model.Executor
	tools               tool.Service
	renderer            prompt.Renderer
	store               session.Store
	sink                EventSink
	maxTurns            int
	maxToolCallsPerTurn int
	stopOnToolError     bool
	toolSpecs           []tool.Spec
	trimmer             trim.Trimmer
	trimBudget          int
}

// NewToolLoopAgent validates options and returns an immutable orchestrator.
func NewToolLoopAgent(options ToolLoopOptions) (*ToolLoopAgent, error) {
	// RunEnv holds the canonical capability set shared by run strategies;
	// assemble it from the direct Option fields and validate through the
	// common helper. Capabilities are bound at construction, never per Run.
	env := RunEnv{
		Executor:  options.Executor,
		Tools:     options.Tools,
		Renderer:  options.Renderer,
		Store:     options.Store,
		EventSink: options.EventSink,
	}
	if err := validateRunEnv(env); err != nil {
		return nil, err
	}

	maxTurns := options.MaxTurns
	if maxTurns <= 0 {
		maxTurns = defaultMaxTurns
	}
	maxToolCalls := options.MaxToolCallsPerTurn
	if maxToolCalls <= 0 {
		maxToolCalls = defaultMaxToolCallsPerTurn
	}

	var specs []tool.Spec
	if env.Tools != nil {
		specs = env.Tools.Specs()
	}

	return &ToolLoopAgent{
		executor:            env.Executor,
		tools:               env.Tools,
		renderer:            env.Renderer,
		store:               env.Store,
		sink:                env.EventSink,
		maxTurns:            maxTurns,
		maxToolCallsPerTurn: maxToolCalls,
		stopOnToolError:     options.StopOnToolError,
		toolSpecs:           specs,
		trimmer:             options.Trimmer,
		trimBudget:          trimBudget(options.Trimmer, options.TrimBudget),
	}, nil
}

// Run executes one ToolLoop run. Safe for concurrent use across goroutines.
func (a *ToolLoopAgent) Run(ctx context.Context, request RunRequest) (RunResult, error) {
	if ctx == nil {
		return RunResult{}, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return RunResult{}, err
	}

	runID, err := newRunID()
	if err != nil {
		return RunResult{}, fmt.Errorf("agent: generate run id: %w", err)
	}
	scope := NewRunScope(RunMeta{
		RunID:     runID,
		SessionID: sessionKeyString(request.SessionKey),
		CreatedAt: time.Now(),
	})

	a.publish(ctx, scope, EventRunStarted, RunStarted{Input: request.Input})
	a.publish(ctx, scope, EventInputReceived, InputReceived{Message: request.Input})

	var systemText string
	if a.renderer != nil {
		text, err := a.renderer.Render(ctx, prompt.Input{Values: request.PromptValues})
		if err != nil {
			return a.fail(ctx, scope, "render_failed", err)
		}
		systemText = text
	}

	var snapshot session.Snapshot
	if a.store != nil {
		loaded, err := a.store.Load(ctx, request.SessionKey)
		if err != nil {
			return a.fail(ctx, scope, "session_load_failed", err)
		}
		snapshot = loaded
	}

	// Optionally trim the loaded session history so a long-running conversation
	// stays within a bounded working set before it is sent to the model. This
	// only affects this run's snapshot; the session store is untouched.
	if a.trimmer != nil && a.store != nil {
		snapshot.Messages = a.trimmer.Trim(snapshot.Messages, a.trimBudget)
	}

	history := append(message.CloneSlice(snapshot.Messages), message.Clone(request.Input))

	for turn := 1; ; turn++ {
		if err := ctx.Err(); err != nil {
			return a.fail(ctx, scope, "context_deadline_exceeded", err)
		}
		if turn > a.maxTurns {
			return a.fail(ctx, scope, "max_turns_exceeded", ErrMaxTurnsExceeded)
		}
		scope.RecordTurn()

		reqMessages := withSystemPrompt(systemText, history)
		a.publish(ctx, scope, EventModelGenerationStarted, ModelGenerationStarted{Model: a.executor.Model()})

		stream, err := a.executor.Generate(ctx, model.Request{Messages: reqMessages, Tools: a.toolSpecs})
		if err != nil {
			return a.fail(ctx, scope, "model_generation_failed", err)
		}
		scope.RecordStep() // one model call is one step

		response, err := model.Collect(ctx, a.observe(ctx, scope, stream))
		if err != nil {
			return a.fail(ctx, scope, "model_generation_failed", err)
		}
		scope.RecordUsage(response.Usage)
		a.publish(ctx, scope, EventModelGenerationFinished, ModelGenerationFinished{
			Model:        a.executor.Model(),
			Message:      response.Message,
			Usage:        response.Usage,
			FinishReason: response.FinishReason,
		})

		history = append(history, response.Message)
		calls := message.ToolCalls(response.Message)

		if len(calls) == 0 {
			revision, err := a.commit(ctx, request.SessionKey, snapshot.Revision, history[len(snapshot.Messages):])
			if err != nil {
				return a.fail(ctx, scope, "session_commit_failed", err)
			}
			a.publish(ctx, scope, EventRunCompleted, RunCompleted{Message: response.Message})
			return RunResult{Message: response.Message, Revision: revision, Stats: scope.Stats()}, nil
		}

		if len(calls) > a.maxToolCallsPerTurn {
			return a.fail(ctx, scope, "too_many_tool_calls", ErrTooManyToolCalls)
		}
		if a.tools == nil {
			return a.fail(ctx, scope, "no_tool_service", ErrNoToolService)
		}

		for _, call := range calls {
			if err := ctx.Err(); err != nil {
				return a.fail(ctx, scope, "context_deadline_exceeded", err)
			}

			toolCall := tool.Call{ID: call.ID, Name: call.Name, Arguments: json.RawMessage(call.Arguments)}
			a.publish(ctx, scope, EventToolCallRequested, ToolCallRequested{Call: toolCall})
			a.publish(ctx, scope, EventToolExecutionStarted, ToolExecutionStarted{Call: toolCall})
			scope.RecordToolCall()
			scope.RecordStep() // one tool call is one step

			start := time.Now()
			result, err := a.tools.Execute(ctx, toolCall)
			duration := time.Since(start)
			if err != nil {
				if a.stopOnToolError {
					return a.fail(ctx, scope, "tool_execution_failed", fmt.Errorf("agent: tool %q: %w", call.Name, err))
				}
				history = append(history, toolErrorMessage(call, err))
				continue
			}
			a.publish(ctx, scope, EventToolExecutionFinished, ToolExecutionFinished{Call: toolCall, Result: result, Duration: duration})
			history = append(history, toolResultMessage(call, result))
		}
	}
}

// fail classifies err as a cancellation or a domain failure, publishes the
// matching terminal event, and returns the (zero, err) result pair expected
// by every Run failure path.
func (a *ToolLoopAgent) fail(ctx context.Context, scope *RunScope, kind string, err error) (RunResult, error) {
	if errors.Is(err, context.Canceled) {
		a.publish(ctx, scope, EventRunCancelled, RunCancelled{})
	} else {
		a.publish(ctx, scope, EventRunFailed, RunFailed{ErrorKind: kind})
	}
	return RunResult{}, err
}

// publish is a best-effort event emission: a nil EventSink or a Publish
// error does not affect the run. Reliable delivery is the responsibility of
// the configured EventSink, not this orchestrator (see
// doc/agent运行领域事件设计方案.md §3.4). The event sequence number is
// allocated from the scope so it stays monotonic within a single run.
func (a *ToolLoopAgent) publish(ctx context.Context, scope *RunScope, eventType EventType, data any) {
	if a.sink == nil {
		return
	}
	sequence := scope.NextSequence()
	_ = a.sink.Publish(ctx, Event{
		RunID:      scope.Meta().RunID,
		Sequence:   sequence,
		OccurredAt: time.Now(),
		Type:       eventType,
		Data:       data,
	})
}

// observe wraps stream so that every received Event with a non-empty Delta
// also publishes an EventModelDelta, without duplicating model.Collect's
// accumulation logic: the wrapped stream is still consumed by model.Collect.
func (a *ToolLoopAgent) observe(ctx context.Context, scope *RunScope, stream model.Stream) model.Stream {
	return observingStream{
		Stream: stream,
		onEvent: func(event model.Event) {
			if event.Delta == "" {
				return
			}
			a.publish(ctx, scope, EventModelDelta, ModelDelta{
				Model:        a.executor.Model(),
				Delta:        event.Delta,
				FinishReason: event.FinishReason,
			})
		},
	}
}

type observingStream struct {
	model.Stream
	onEvent func(model.Event)
}

func (s observingStream) Recv(ctx context.Context) (model.Event, error) {
	event, err := s.Stream.Recv(ctx)
	if err != nil {
		return event, err
	}
	s.onEvent(event)
	return event, nil
}

// commit appends the run's new messages to Session in one batch. It is a
// no-op returning revision zero when no Store is configured.
func (a *ToolLoopAgent) commit(ctx context.Context, key session.Key, expected session.Revision, batch []message.Message) (session.Revision, error) {
	if a.store == nil {
		return 0, nil
	}
	return a.store.Append(ctx, key, expected, batch)
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

func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// sessionKeyString renders a deterministic identifier string for a session
// key, used as the run's SessionID. It returns "" when the key has no usable
// identity (e.g. stateless runs without a configured session store).
func sessionKeyString(key session.Key) string {
	if key.Scope == "" && key.ID == "" {
		return ""
	}
	return key.Scope + "/" + key.ID
}
