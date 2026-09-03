package event

import (
	"time"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/tool"
)

// EventType identifies a provider-independent Agent runtime event.
type EventType string

const (
	EventRunStarted              EventType = "run.started"
	EventInputReceived           EventType = "input.received"
	EventModelGenerationStarted  EventType = "model.generation_started"
	EventModelDelta              EventType = "model.delta"
	EventModelGenerationFinished EventType = "model.generation_finished"
	EventToolCallRequested       EventType = "tool.call_requested"
	EventToolExecutionStarted    EventType = "tool.execution_started"
	EventToolExecutionFinished   EventType = "tool.execution_finished"
	EventRunCompleted            EventType = "run.completed"
	EventRunFailed               EventType = "run.failed"
	EventRunCancelled            EventType = "run.cancelled"
)

// Event is one ordered event produced by an Agent run.
type Event struct {
	RunID      string
	Sequence   uint64
	OccurredAt time.Time
	Type       EventType
	Data       any
}

// RunStarted describes the beginning of an Agent run.
type RunStarted struct{ Input message.Message }

// InputReceived describes the input accepted by an Agent run.
type InputReceived struct{ Message message.Message }

// ModelGenerationStarted describes the beginning of one model call.
type ModelGenerationStarted struct{ Model model.Ref }

// ModelDelta describes one incremental model output.
type ModelDelta struct {
	Model        model.Ref
	Delta        string
	FinishReason model.FinishReason
}

// ModelGenerationFinished describes the completed output of one model call.
type ModelGenerationFinished struct {
	Model        model.Ref
	Message      message.Message
	Usage        model.Usage
	FinishReason model.FinishReason
}

// ToolCallRequested describes a tool invocation requested by a model.
type ToolCallRequested struct{ Call tool.Call }

// ToolExecutionStarted describes the beginning of one tool invocation.
type ToolExecutionStarted struct{ Call tool.Call }

// ToolExecutionFinished describes a successful tool invocation.
type ToolExecutionFinished struct {
	Call     tool.Call
	Result   tool.Result
	Duration time.Duration
}

// RunCompleted describes a successfully completed Agent run.
type RunCompleted struct{ Message message.Message }

// RunFailed describes an Agent run that ended with an error.
type RunFailed struct{ ErrorKind string }

// RunCancelled describes an Agent run cancelled by its context or policy.
type RunCancelled struct{}
