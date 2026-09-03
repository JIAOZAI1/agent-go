package trim

import (
	"github.com/JIAOZAI1/agent-go/message"
)

// Trimmer processes one loaded conversation snapshot before it enters a model
// request. It must be a pure operation: it must not retain or mutate its input
// (callers pass isolated snapshots), and concurrent calls with independent
// snapshots must be safe.
//
// Implementations are expected to keep the most recent working context while
// dropping or compressing older messages so the result fits a budget expressed
// in the same unit as SizeOf (bytes of content).
type Trimmer interface {
	// Trim returns the history to send for the conversation current (oldest
	// first). It may return current itself, a strict sub-slice, or a newly
	// built slice; it must not retain a reference that outlives the call.
	Trim(current []message.Message, budget int) []message.Message
}

// TrimmerFunc adapts an ordinary function to the Trimmer interface. A nil
// receiver treats every call as a no-op returning current unchanged.
type TrimmerFunc func(current []message.Message, budget int) []message.Message

// Trim implements Trimmer by invoking the wrapped function.
func (f TrimmerFunc) Trim(current []message.Message, budget int) []message.Message {
	if f == nil {
		return current
	}
	return f(current, budget)
}
