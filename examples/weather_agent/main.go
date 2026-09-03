// Command weather_agent demonstrates composing agent-go's public components
// into a tool-calling agent loop entirely offline (no external model provider
// and no API key). It wires a ToolLoopAgent with an in-memory session store, a
// small tool catalog, and a FanoutSink that prints lifecycle events, driven by
// a fake Executor that replays a scripted weather-tool conversation.
//
// To point the same wiring at a real model later, replace createExecutor with
// openai.NewExecutor and provide OPENAI_API_KEY; nothing else changes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
)

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func main() {
	ctx := context.Background()

	executor, err := newScriptedExecutor()
	if err != nil {
		fail("executor: %v", err)
	}

	registryBuilder := tool.NewBuilder()
	if err := registryBuilder.AddMiddleware(logToolMiddleware); err != nil {
		fail("add middleware: %v", err)
	}
	if err := registryBuilder.AddTool(weatherTool{}); err != nil {
		fail("add tool: %v", err)
	}
	registry, err := registryBuilder.Build()
	if err != nil {
		fail("build tools: %v", err)
	}

	store := session.NewMemoryStore()
	key := session.Key{Scope: "examples", ID: "weather-1"}

	// A FanoutSink prints every life-cycle event to stdout, demonstrating the
	// observable side of a run.
	sink := agent.NewFanoutSink(agent.FanoutOptions{Overflow: agent.OverflowDropNewest})
	_, err = sink.Subscribe("stdout", func(_ context.Context, event agent.Event) error {
		fmt.Printf("event %-3d %-28s\n", event.Sequence, event.Type)
		return nil
	})
	if err != nil {
		fail("subscribe: %v", err)
	}
	defer sink.Close()

	loop, err := agent.NewToolLoopAgent(agent.ToolLoopOptions{
		Executor:  executor,
		Tools:     registry,
		Store:     store,
		EventSink: sink,
	})
	if err != nil {
		fail("new agent: %v", err)
	}

	result, err := loop.Run(ctx, agent.RunRequest{
		SessionKey: key,
		Input:      message.Text(message.RoleUser, "今天北京天气怎么样？"),
	})
	if err != nil {
		fail("run agent: %v", err)
	}

	fmt.Printf("\nfinal reply: %s\n", result.Message.Content[0].Text)
	fmt.Printf("turn=%d step=%d tool=%d\n",
		result.Stats.TurnCount, result.Stats.StepCount, result.Stats.ToolCallCount)
	fmt.Printf("session revision=%d (history is stored across runs)\n", result.Revision)
}

// weatherTool is a demo tool: given a city, returns a canned temperature.
type weatherTool struct{}

func (weatherTool) Spec() tool.Spec {
	return tool.Spec{
		Name:        "get_weather",
		Description: "返回指定城市的当前天气。",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
	}
}

func (weatherTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var args struct {
		City string `json:"city"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tool.Result{}, fmt.Errorf("bad arguments: %w", err)
	}
	return tool.Result{Content: fmt.Sprintf("%s: 晴, 26°C", args.City)}, nil
}

// logToolMiddleware demonstrates the tool middleware chain bound at build time.
func logToolMiddleware(ctx context.Context, call tool.Call, next tool.ExecuteNext) (tool.Result, error) {
	fmt.Printf("tool executing: %s\n", call.Name)
	return next(ctx, call)
}

// After the first Generate returns a tool call, the live loop executes the tool
// and calls back in with its result; the count is bumped so later calls yield a
// final stop turn before MaxTurns is reached.
type scriptedExecutor struct {
	ref       model.Ref
	callCount int
}

func newScriptedExecutor() (*scriptedExecutor, error) {
	return &scriptedExecutor{ref: model.Ref{ProviderID: "demo", ModelID: "demo-model"}}, nil
}

func (e *scriptedExecutor) Model() model.Ref { return e.ref }

func (e *scriptedExecutor) Generate(_ context.Context, request model.Request) (model.Stream, error) {
	e.callCount++
	if e.callCount == 1 {
		toolCall := message.ToolCall{
			ID:        "call-weather-1",
			Name:      "get_weather",
			Arguments: json.RawMessage(`{"city":"北京"}`),
		}
		return &scriptedStream{events: []model.Event{
			{Delta: "需要查询天气。"},
			{ToolCall: &toolCall},
			{FinishReason: model.FinishToolCall},
		}}, nil
	}
	return &scriptedStream{events: []model.Event{
		{Delta: "北京今天晴，26°C。"},
		{FinishReason: model.FinishStop},
	}}, nil
}

type scriptedStream struct {
	events []model.Event
	index  int
}

func (s *scriptedStream) Recv(context.Context) (model.Event, error) {
	if s.index == len(s.events) {
		return model.Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scriptedStream) Close() error { return nil }
