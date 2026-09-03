package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/JIAOZAI1/agent-go/tool"
)

type testTool struct {
	spec    tool.Spec
	execute func(context.Context, json.RawMessage) (tool.Result, error)
}

func (t testTool) Spec() tool.Spec { return t.spec }

func (t testTool) Execute(ctx context.Context, arguments json.RawMessage) (tool.Result, error) {
	return t.execute(ctx, arguments)
}

func newTestTool(name string, execute func(context.Context, json.RawMessage) (tool.Result, error)) testTool {
	return testTool{spec: tool.Spec{Name: name, Description: name, Parameters: json.RawMessage(`{"type":"object"}`)}, execute: execute}
}

func TestRuntimeExecutesAndCopies(t *testing.T) {
	var received json.RawMessage
	builder := tool.NewBuilder()
	if err := builder.AddTool(newTestTool("search", func(_ context.Context, arguments json.RawMessage) (tool.Result, error) {
		received = arguments
		return tool.Result{Content: "ok", Metadata: tool.ResultMetadata{ToolName: "fake"}}, nil
	})); err != nil {
		t.Fatal(err)
	}
	runtime, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	arguments := json.RawMessage(`{"query":"go"}`)
	result, err := runtime.Execute(context.Background(), tool.Call{ID: "call-1", Name: "search", Arguments: arguments})
	if err != nil || result.Content != "ok" {
		t.Fatalf("Execute() = %+v, %v", result, err)
	}
	arguments[2] = 'X'
	if string(received) != `{"query":"go"}` {
		t.Fatalf("received arguments = %s", received)
	}
	if result.Metadata.CallID != "call-1" || result.Metadata.ToolName != "search" || !result.Metadata.Success || result.Metadata.EndTimeMS < result.Metadata.StartTimeMS {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

func TestBuilderValidationAndSpecs(t *testing.T) {
	builder := tool.NewBuilder()
	if err := builder.AddTool(newTestTool("one", func(context.Context, json.RawMessage) (tool.Result, error) { return tool.Result{}, nil })); err != nil {
		t.Fatal(err)
	}
	if err := builder.AddTool(newTestTool("one", nil)); !errors.Is(err, tool.ErrDuplicateTool) {
		t.Fatalf("duplicate error = %v", err)
	}
	runtime, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.AddMiddleware(func(context.Context, tool.Call, tool.ExecuteNext) (tool.Result, error) { return tool.Result{}, nil }); !errors.Is(err, tool.ErrBuilderBuilt) {
		t.Fatalf("late middleware error = %v", err)
	}
	specs := runtime.Specs()
	specs[0].Parameters[0] = '['
	if reflect.DeepEqual(specs, runtime.Specs()) {
		t.Fatal("Specs() did not return independent schemas")
	}
}

func TestRuntimeMiddlewareOrderAndShortCircuit(t *testing.T) {
	order := make([]string, 0, 4)
	builder := tool.NewBuilder()
	if err := builder.AddTool(newTestTool("run", func(context.Context, json.RawMessage) (tool.Result, error) {
		order = append(order, "tool")
		return tool.Result{Content: "tool"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		name := name
		if err := builder.AddMiddleware(func(ctx context.Context, call tool.Call, next tool.ExecuteNext) (tool.Result, error) {
			order = append(order, name+"+")
			result, err := next(ctx, call)
			order = append(order, name+"-")
			return result, err
		}); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := builder.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Execute(context.Background(), tool.Call{Name: "run", Arguments: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a+", "b+", "tool", "b-", "a-"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}

	short := tool.NewBuilder()
	called := false
	if err := short.AddTool(newTestTool("run", func(context.Context, json.RawMessage) (tool.Result, error) { called = true; return tool.Result{}, nil })); err != nil {
		t.Fatal(err)
	}
	if err := short.AddMiddleware(func(context.Context, tool.Call, tool.ExecuteNext) (tool.Result, error) {
		return tool.Result{Content: "blocked"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	shortRuntime, err := short.Build()
	if err != nil {
		t.Fatal(err)
	}
	result, err := shortRuntime.Execute(context.Background(), tool.Call{Name: "run", Arguments: json.RawMessage(`{}`)})
	if err != nil || result.Content != "blocked" || called {
		t.Fatalf("short circuit = %+v, %v, called=%v", result, err, called)
	}
}

func TestRuntimeErrors(t *testing.T) {
	runtime, err := tool.NewBuilder().Build()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		ctx  context.Context
		call tool.Call
		want error
	}{
		{name: "nil context", want: tool.ErrInvalidContext, call: tool.Call{Name: "x", Arguments: json.RawMessage(`{}`)}},
		{name: "empty name", ctx: context.Background(), want: tool.ErrEmptyToolName, call: tool.Call{Arguments: json.RawMessage(`{}`)}},
		{name: "invalid args", ctx: context.Background(), want: tool.ErrInvalidArguments, call: tool.Call{Name: "x", Arguments: json.RawMessage(`[]`)}},
		{name: "not found", ctx: context.Background(), want: tool.ErrToolNotFound, call: tool.Call{Name: "x", Arguments: json.RawMessage(`{}`)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotCtx := test.ctx
			_, gotErr := runtime.Execute(gotCtx, test.call)
			if !errors.Is(gotErr, test.want) {
				t.Fatalf("error = %v, want %v", gotErr, test.want)
			}
		})
	}
}

func TestRuntimeSupportsConcurrentExecution(t *testing.T) {
	var executions atomic.Int32
	runtime, err := func() (*tool.ToolRuntime, error) {
		builder := tool.NewBuilder()
		if err := builder.AddTool(newTestTool("parallel", func(context.Context, json.RawMessage) (tool.Result, error) {
			executions.Add(1)
			return tool.Result{Content: "ok"}, nil
		})); err != nil {
			return nil, err
		}
		return builder.Build()
	}()
	if err != nil {
		t.Fatal(err)
	}

	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			if _, err := runtime.Execute(context.Background(), tool.Call{Name: "parallel", Arguments: json.RawMessage(`{}`)}); err != nil {
				t.Errorf("Execute() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if got := executions.Load(); got != workers {
		t.Fatalf("executions = %d, want %d", got, workers)
	}
}
