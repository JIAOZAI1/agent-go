package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
)

// scriptedTurn is one model reply a fakeExecutor returns for one Generate call.
type scriptedTurn struct {
	message      message.Message
	finishReason model.FinishReason
	err          error
}

// fakeExecutor replays a fixed script of turns; calls beyond the script
// repeat the last scripted turn.
type fakeExecutor struct {
	ref   model.Ref
	turns []scriptedTurn
	mu    sync.Mutex
	calls int
}

func (e *fakeExecutor) Model() model.Ref { return e.ref }

func (e *fakeExecutor) Generate(ctx context.Context, _ model.Request) (model.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	index := e.calls
	if index >= len(e.turns) {
		index = len(e.turns) - 1
	}
	e.calls++
	e.mu.Unlock()

	turn := e.turns[index]
	if turn.err != nil {
		return nil, turn.err
	}
	return &fakeStream{events: turnToEvents(turn)}, nil
}

func turnToEvents(turn scriptedTurn) []model.Event {
	var events []model.Event
	for _, block := range turn.message.Content {
		switch block.Kind {
		case message.ContentText:
			events = append(events, model.Event{Delta: block.Text})
		case message.ContentToolCall:
			call := *block.ToolCall
			events = append(events, model.Event{ToolCall: &call})
		}
	}
	events = append(events, model.Event{FinishReason: turn.finishReason})
	return events
}

type fakeStream struct {
	events []model.Event
	index  int
}

func (s *fakeStream) Recv(ctx context.Context) (model.Event, error) {
	if err := ctx.Err(); err != nil {
		return model.Event{}, err
	}
	if s.index == len(s.events) {
		return model.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *fakeStream) Close() error { return nil }

// staticExecutor always returns the same one-shot text reply. It carries no
// mutable state, so it is safe to share across concurrent Run calls.
type staticExecutor struct {
	ref  model.Ref
	text string
}

func (e staticExecutor) Model() model.Ref { return e.ref }

func (e staticExecutor) Generate(ctx context.Context, _ model.Request) (model.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &fakeStream{events: []model.Event{{Delta: e.text}, {FinishReason: model.FinishStop}}}, nil
}

// fakeTools is a tool.Service test double keyed by tool name.
type fakeTools struct {
	specs   []tool.Spec
	results map[string]tool.Result
	errs    map[string]error

	mu    sync.Mutex
	calls []tool.Call
}

func (t *fakeTools) Specs() []tool.Spec { return t.specs }

func (t *fakeTools) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	t.mu.Lock()
	t.calls = append(t.calls, call)
	t.mu.Unlock()
	if err, ok := t.errs[call.Name]; ok {
		return tool.Result{}, err
	}
	return t.results[call.Name], nil
}

// recordingSink is a concurrency-safe EventSink test double.
type recordingSink struct {
	mu     sync.Mutex
	events []agent.Event
}

func (s *recordingSink) Publish(_ context.Context, event agent.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *recordingSink) types() []agent.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]agent.EventType, len(s.events))
	for index, event := range s.events {
		result[index] = event.Type
	}
	return result
}

// conflictStore always reports a session conflict on Append.
type conflictStore struct{}

func (conflictStore) Load(context.Context, session.Key) (session.Snapshot, error) {
	return session.Snapshot{}, nil
}

func (conflictStore) Append(context.Context, session.Key, session.Revision, []message.Message) (session.Revision, error) {
	return 0, session.ErrConflict
}

func assistantText(text string) message.Message {
	return message.Text(message.RoleAssistant, text)
}

func assistantToolCall(id, name, arguments string) message.Message {
	return message.Message{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{
			{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(arguments)}},
		},
	}
}

func TestToolLoopAgentSingleTurnNoTools(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{{message: assistantText("hi"), finishReason: model.FinishStop}}}
	store := session.NewMemoryStore()
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Store: store, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	key := session.Key{Scope: "test", ID: "single"}
	result, err := loop.Run(context.Background(), agent.RunRequest{SessionKey: key, Input: message.Text(message.RoleUser, "hello")})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(result.Message.Content) != 1 || result.Message.Content[0].Text != "hi" {
		t.Fatalf("Run() message = %+v, want text 'hi'", result.Message)
	}
	if result.Revision != 1 {
		t.Fatalf("Run() revision = %d, want 1", result.Revision)
	}

	snapshot, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("session has %d messages, want 2 (user input + assistant reply)", len(snapshot.Messages))
	}

	want := []agent.EventType{
		agent.EventRunStarted, agent.EventInputReceived,
		agent.EventModelGenerationStarted, agent.EventModelDelta, agent.EventModelGenerationFinished,
		agent.EventRunCompleted,
	}
	assertEventSequence(t, sink.types(), want)
}

func TestToolLoopAgentMultiTurnToolCall(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "get_weather", `{"city":"sf"}`), finishReason: model.FinishToolCall},
		{message: assistantText("sunny"), finishReason: model.FinishStop},
	}}
	tools := &fakeTools{
		specs:   []tool.Spec{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		results: map[string]tool.Result{"get_weather": {Content: "72F sunny"}},
	}
	store := session.NewMemoryStore()
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, Store: store, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	key := session.Key{Scope: "test", ID: "multi"}
	result, err := loop.Run(context.Background(), agent.RunRequest{SessionKey: key, Input: message.Text(message.RoleUser, "weather?")})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Message.Content[0].Text != "sunny" {
		t.Fatalf("Run() message = %+v, want text 'sunny'", result.Message)
	}
	if len(tools.calls) != 1 || tools.calls[0].ID != "call-1" || tools.calls[0].Name != "get_weather" {
		t.Fatalf("tools.calls = %+v, want one get_weather call with ID call-1", tools.calls)
	}

	snapshot, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(snapshot.Messages) != 4 {
		t.Fatalf("session has %d messages, want 4 (input, assistant tool call, tool result, assistant final)", len(snapshot.Messages))
	}
	if snapshot.Messages[2].Role != message.RoleTool || snapshot.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("tool result message = %+v, want role=tool tool_call_id=call-1", snapshot.Messages[2])
	}
	if snapshot.Revision != 1 {
		t.Fatalf("session committed %d times, want exactly 1 (one Append for the whole run)", snapshot.Revision)
	}

	want := []agent.EventType{
		agent.EventRunStarted, agent.EventInputReceived,
		agent.EventModelGenerationStarted, agent.EventModelGenerationFinished,
		agent.EventToolCallRequested, agent.EventToolExecutionStarted, agent.EventToolExecutionFinished,
		agent.EventModelGenerationStarted, agent.EventModelDelta, agent.EventModelGenerationFinished,
		agent.EventRunCompleted,
	}
	assertEventSequence(t, sink.types(), want)
}

func TestToolLoopAgentMaxTurnsExceeded(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "noop", `{}`), finishReason: model.FinishToolCall},
	}}
	tools := &fakeTools{
		specs:   []tool.Spec{{Name: "noop", Parameters: json.RawMessage(`{}`)}},
		results: map[string]tool.Result{"noop": {Content: "ok"}},
	}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, EventSink: sink, MaxTurns: 2})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	_, err = loop.Run(context.Background(), agent.RunRequest{Input: message.Text(message.RoleUser, "go")})
	if !errors.Is(err, agent.ErrMaxTurnsExceeded) {
		t.Fatalf("Run() error = %v, want ErrMaxTurnsExceeded", err)
	}

	types := sink.types()
	if types[len(types)-1] != agent.EventRunFailed {
		t.Fatalf("last event = %v, want RunFailed", types[len(types)-1])
	}
}

func TestToolLoopAgentTooManyToolCalls(t *testing.T) {
	tooMany := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{
		{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "1", Name: "f", Arguments: json.RawMessage(`{}`)}},
		{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "2", Name: "f", Arguments: json.RawMessage(`{}`)}},
		{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "3", Name: "f", Arguments: json.RawMessage(`{}`)}},
	}}
	executor := &fakeExecutor{turns: []scriptedTurn{{message: tooMany, finishReason: model.FinishToolCall}}}
	tools := &fakeTools{specs: []tool.Spec{{Name: "f", Parameters: json.RawMessage(`{}`)}}, results: map[string]tool.Result{"f": {Content: "ok"}}}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, MaxToolCallsPerTurn: 2})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	_, err = loop.Run(context.Background(), agent.RunRequest{Input: message.Text(message.RoleUser, "go")})
	if !errors.Is(err, agent.ErrTooManyToolCalls) {
		t.Fatalf("Run() error = %v, want ErrTooManyToolCalls", err)
	}
}

func TestToolLoopAgentToolErrorDefaultContinues(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "flaky", `{}`), finishReason: model.FinishToolCall},
		{message: assistantText("recovered"), finishReason: model.FinishStop},
	}}
	tools := &fakeTools{
		specs: []tool.Spec{{Name: "flaky", Parameters: json.RawMessage(`{}`)}},
		errs:  map[string]error{"flaky": errors.New("boom")},
	}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	result, err := loop.Run(context.Background(), agent.RunRequest{Input: message.Text(message.RoleUser, "go")})
	if err != nil {
		t.Fatalf("Run() error = %v, want success after model recovers", err)
	}
	if result.Message.Content[0].Text != "recovered" {
		t.Fatalf("Run() message = %+v, want text 'recovered'", result.Message)
	}
	for _, eventType := range sink.types() {
		if eventType == agent.EventToolExecutionFinished {
			t.Fatal("got EventToolExecutionFinished for a failed tool call, want it only on success")
		}
	}
}

func TestToolLoopAgentStopOnToolErrorAborts(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "flaky", `{}`), finishReason: model.FinishToolCall},
	}}
	tools := &fakeTools{
		specs: []tool.Spec{{Name: "flaky", Parameters: json.RawMessage(`{}`)}},
		errs:  map[string]error{"flaky": errors.New("boom")},
	}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, EventSink: sink, StopOnToolError: true})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	_, err = loop.Run(context.Background(), agent.RunRequest{Input: message.Text(message.RoleUser, "go")})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Run() error = %v, want an error wrapping 'boom'", err)
	}
	types := sink.types()
	if types[len(types)-1] != agent.EventRunFailed {
		t.Fatalf("last event = %v, want RunFailed", types[len(types)-1])
	}
}

func TestToolLoopAgentNilContext(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{{message: assistantText("hi"), finishReason: model.FinishStop}}}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}
	_, err = loop.Run(nil, agent.RunRequest{Input: message.Text(message.RoleUser, "hi")}) //nolint:staticcheck
	if !errors.Is(err, agent.ErrInvalidContext) {
		t.Fatalf("Run(nil ctx) error = %v, want ErrInvalidContext", err)
	}
}

func TestToolLoopAgentAlreadyCancelledContext(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{{message: assistantText("hi"), finishReason: model.FinishStop}}}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = loop.Run(ctx, agent.RunRequest{Input: message.Text(message.RoleUser, "hi")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(sink.types()) != 0 {
		t.Fatalf("got %d events for a run that never started, want 0", len(sink.types()))
	}
}

// TestToolLoopAgentCancelledDuringLoopPublishesRunCancelled cancels ctx from
// inside a tool execution, so the loop's next per-turn ctx.Err() check trips
// before issuing another Generate call.
func TestToolLoopAgentCancelledDuringLoopPublishesRunCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "cancel_after", `{}`), finishReason: model.FinishToolCall},
	}}
	tools := &cancelingTools{
		fakeTools: &fakeTools{specs: []tool.Spec{{Name: "cancel_after", Parameters: json.RawMessage(`{}`)}}},
		cancel:    cancel,
	}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	_, err = loop.Run(ctx, agent.RunRequest{Input: message.Text(message.RoleUser, "go")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	types := sink.types()
	if types[len(types)-1] != agent.EventRunCancelled {
		t.Fatalf("last event = %v, want RunCancelled", types[len(types)-1])
	}
}

type cancelingTools struct {
	*fakeTools
	cancel context.CancelFunc
}

func (t *cancelingTools) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	t.cancel()
	return tool.Result{Content: "ok"}, nil
}

func TestToolLoopAgentSessionConflict(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{{message: assistantText("hi"), finishReason: model.FinishStop}}}
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Store: conflictStore{}, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	_, err = loop.Run(context.Background(), agent.RunRequest{SessionKey: session.Key{Scope: "s", ID: "1"}, Input: message.Text(message.RoleUser, "hi")})
	if !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Run() error = %v, want session.ErrConflict", err)
	}
	types := sink.types()
	if types[len(types)-1] != agent.EventRunFailed {
		t.Fatalf("last event = %v, want RunFailed", types[len(types)-1])
	}
}

func TestToolLoopAgentConcurrentRuns(t *testing.T) {
	executor := staticExecutor{text: "ok"}
	store := session.NewMemoryStore()
	sink := &recordingSink{}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Store: store, EventSink: sink})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	const goroutines = 20
	var wait sync.WaitGroup
	wait.Add(goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer wait.Done()
			key := session.Key{Scope: "concurrent", ID: string(rune('a' + index))}
			_, err := loop.Run(context.Background(), agent.RunRequest{SessionKey: key, Input: message.Text(message.RoleUser, "hi")})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Run() error = %v", err)
	}
}

func TestToolLoopAgentTracksRunStats(t *testing.T) {
	executor := &fakeExecutor{turns: []scriptedTurn{
		{message: assistantToolCall("call-1", "get_weather", `{"city":"sf"}`), finishReason: model.FinishToolCall},
		{message: assistantText("sunny"), finishReason: model.FinishStop},
	}}
	tools := &fakeTools{
		specs:   []tool.Spec{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
		results: map[string]tool.Result{"get_weather": {Content: "72F sunny"}},
	}
	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{Executor: executor, Tools: tools, EventSink: &recordingSink{}})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	result, err := loop.Run(context.Background(), agent.RunRequest{
		SessionKey: session.Key{Scope: "test", ID: "stats"},
		Input:      message.Text(message.RoleUser, "weather?"),
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Turn1: model generate (step) + one tool call (step + toolcount);
	// Turn2: final model generate (step) with no tools.
	stats := result.Stats
	if stats.TurnCount != 2 {
		t.Errorf("TurnCount = %d, want 2", stats.TurnCount)
	}
	if stats.StepCount != 3 {
		t.Errorf("StepCount = %d, want 3 (generate, tool call, generate)", stats.StepCount)
	}
	if stats.ToolCallCount != 1 {
		t.Errorf("ToolCallCount = %d, want 1", stats.ToolCallCount)
	}
	if result.Message.Content == nil || result.Message.Content[0].Text != "sunny" {
		t.Fatalf("Run() message = %+v, want final text", result.Message)
	}
}

func assertEventSequence(t *testing.T, got, want []agent.EventType) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("event[%d] = %v, want %v (full sequence: got=%v want=%v)", index, got[index], want[index], got, want)
		}
	}
}
