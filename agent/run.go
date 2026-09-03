package agent

import (
	"context"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/prompt"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/tool"
	"github.com/JIAOZAI1/agent-go/trim"
)

// RunEnv is the shared capability set held by every run strategy. Concrete
// strategies (e.g. ToolLoopAgent) read it at construction time and keep an
// immutable copy; sharing RunEnv across strategies removes duplicated
// capability fields from each options struct. Capabilities are bound at
// construction time, never overridden per Run call.
type RunEnv struct {
	Executor  model.Executor  // required
	Tools     tool.Service    // optional; nil means no tool capability
	Renderer  prompt.Renderer // optional; nil means no system prompt
	Store     session.Store   // optional; nil means stateless single-turn
	EventSink EventSink       // optional; nil means no events
	Trimmer   trim.Trimmer    // optional; bounds the loaded history sent each turn; nil means send it untouched
}

// validateRunEnv validates a RunEnv shared by every strategy constructor.
// Only Executor is required.
func validateRunEnv(env RunEnv) error {
	if env.Executor == nil {
		return ErrNilExecutor
	}
	return nil
}

// RunRequest is the input contract for one run executed by any run strategy:
// it identifies the owning session, carries the render variables, and the
// input. SessionKey is required when a Store is configured; it is ignored
// when Store is nil (stateless single-turn).
type RunRequest struct {
	SessionKey   session.Key
	PromptValues prompt.Values
	Input        message.Message
}

// RunResult is the output contract of one completed run.
type RunResult struct {
	Message  message.Message  // final reply
	Revision session.Revision // zero when no Store is configured
	Stats    RunStats         // end-of-run counters / token usage snapshot
}

// runRunner is the unexported unified entry that every run strategy is
// expected to satisfy. It is kept as an internal self-check (rather than an
// exported L3 interface) until a second concrete strategy and a real need to
// dispatch among strategies appear; consumers may declare an equivalent
// small interface locally and assert implementations satisfy it.
type runRunner interface {
	Run(context.Context, RunRequest) (RunResult, error)
}

var _ runRunner = (*ToolLoopAgent)(nil)
