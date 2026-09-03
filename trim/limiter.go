package trim

import "github.com/JIAOZAI1/agent-go/message"

// SizeOf reports the byte size of m's model-facing content, used by size-based
// limiters. It sums text content, image URL/base64 payloads, and tool-call
// names + argument bytes. It deliberately ignores role tags and fixed framing,
// which are tiny versus real content; callers trimming to a hard provider limit
// should treat the returned value as a floor and leave headroom.
func SizeOf(m message.Message) int {
	total := 0
	for _, block := range m.Content {
		total += len(block.Text)
		total += len(block.URL)
		total += len(block.Data)
		if block.ToolCall != nil {
			total += len(block.ToolCall.ID)
			total += len(block.ToolCall.Name)
			total += len(block.ToolCall.Arguments)
		}
	}
	return total
}

// KeepRecent returns the longest suffix of current (in original order) whose
// cumulative SizeOf does not exceed budget, dropping messages from the oldest
// end first. It always keeps at least the newest message when current is
// non-empty, even if that single message alone exceeds budget, so the returned
// history is never empty for a non-empty input.
//
// An empty current yields nil. The returned slice references the same
// underlying messages as current; it is a shallow re-slice, so callers that
// send the result onward must treat the messages as read-only or copy them.
func KeepRecent(current []message.Message, budget int) []message.Message {
	if len(current) == 0 {
		return nil
	}
	if budget <= 0 {
		// Can't fit anything meaningful; keep the newest message only so the
		// request stays valid and the model at least sees the latest turn.
		return current[len(current)-1:]
	}

	size := 0
	start := len(current)
	for index := len(current) - 1; index >= 0; index-- {
		next := size + SizeOf(current[index])
		if index < len(current)-1 && next > budget {
			break
		}
		size = next
		start = index
	}
	return current[start:]
}

// NewKeepRecent returns a Trimmer that keeps the most recent conversation
// within a budgeted byte size, useful as a wiring-default for agents that want
// bounded context without extra provider-specific tokenizers.
func NewKeepRecent() Trimmer {
	return TrimmerFunc(func(current []message.Message, budget int) []message.Message {
		return KeepRecent(current, budget)
	})
}
