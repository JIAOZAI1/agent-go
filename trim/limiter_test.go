package trim_test

import (
	"strings"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/trim"
)

func textMsg(role message.Role, text string) message.Message {
	return message.Text(role, text)
}

func TestSizeOfText(t *testing.T) {
	if got := trim.SizeOf(textMsg(message.RoleUser, "abcd")); got != 4 {
		t.Fatalf("SizeOf(text 4 bytes) = %d, want 4", got)
	}
}

func TestSizeOfBlank(t *testing.T) {
	msg := message.Text(message.RoleUser, "abcd")
	if got := trim.SizeOf(message.Message{}); got != 0 {
		t.Fatalf("SizeOf(empty) = %d, want 0", got)
	}
	if got := trim.SizeOf(msg); got != 4 {
		t.Fatalf("SizeOf('abcd') = %d, want 4", got)
	}
}

func TestSizeOfImageAndToolCall(t *testing.T) {
	image := message.Message{Role: message.RoleUser, Content: []message.ContentBlock{
		{Kind: message.ContentImage, URL: "http://x/a/img.png", Data: "AAAA"},
	}}
	if got := trim.SizeOf(image); got != len("http://x/a/img.png")+4 {
		t.Fatalf("SizeOf(image) = %d, want url+data length", got)
	}

	toolCall := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{
		{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{ID: "1", Name: "get", Arguments: []byte(`{"a":1}`)}},
	}}
	if got := trim.SizeOf(toolCall); got != 1+len("get")+len(`{"a":1}`) {
		t.Fatalf("SizeOf(toolcall) = %d, want id+name+args bytes", got)
	}
}

func TestKeepRecentEmpty(t *testing.T) {
	if got := trim.KeepRecent(nil, 100); got != nil {
		t.Fatalf("KeepRecent(nil) = %v, want nil", got)
	}
}

// buildHistory returns n messages each with 'a' repeated size bytes.
func buildHistory(n, size int) []message.Message {
	pad := strings.Repeat("a", size)
	history := make([]message.Message, n)
	for index := range history {
		history[index] = textMsg(message.RoleUser, pad)
	}
	return history
}

func TestKeepRecentUnderBudgetUnchanged(t *testing.T) {
	history := buildHistory(3, 10) // total 30
	got := trim.KeepRecent(history, 1000)
	if len(got) != 3 {
		t.Fatalf("KeepRecent under budget = %d messages, want 3 unchanged", len(got))
	}
}

func TestKeepRecentDropsOldest(t *testing.T) {
	history := buildHistory(5, 10)      // total 50
	got := trim.KeepRecent(history, 31) // fits newest 3 (30), drops first 2
	if len(got) != 3 {
		t.Fatalf("KeepRecent budget 31 = %d messages, want 3", len(got))
	}
	// Result must reference the newest three in order.
	if &got[0] != &history[2] {
		t.Fatalf("KeepRecent did not keep the newest suffix")
	}
}

func TestKeepRecentExactBudget(t *testing.T) {
	history := buildHistory(4, 10) // 40
	got := trim.KeepRecent(history, 40)
	if len(got) != 4 {
		t.Fatalf("KeepRecent exact budget = %d messages, want 4", len(got))
	}
}

func TestKeepRecentNonEmptyWhenSingleExceedsBudget(t *testing.T) {
	history := buildHistory(1, 50) // one message of 50
	got := trim.KeepRecent(history, 10)
	if len(got) != 1 || got[0].Content[0].Text != strings.Repeat("a", 50) {
		t.Fatalf("KeepRecent when single message exceeds budget = %+v, want the single newest kept", got)
	}
}

func TestKeepRecentAfterMultiAppend(t *testing.T) {
	// Simulate unbounded growth: 1000 turns at 20 bytes each.
	history := buildHistory(1000, 20) // 20000 bytes
	got := trim.KeepRecent(history, 200)
	if len(got) == 0 {
		t.Fatal("KeepRecent returned empty for non-empty input")
	}
	var size int
	for _, m := range got {
		size += trim.SizeOf(m)
	}
	if size > 200 {
		t.Fatalf("KeepRecent result size %d exceeds budget 200", size)
	}
	// The newest message must be preserved.
	if &got[len(got)-1] != &history[len(history)-1] {
		t.Fatal("KeepRecent did not preserve the newest message")
	}
}

func TestTrimmerFuncNilIsNoop(t *testing.T) {
	var tf trim.TrimmerFunc
	history := buildHistory(2, 5)
	if got := tf.Trim(history, 1); len(got) != 2 {
		t.Fatalf("nil TrimmerFunc replaced history = %d messages, want unchanged", len(got))
	}
}

func TestNewKeepRecent(t *testing.T) {
	tr := trim.NewKeepRecent()
	history := buildHistory(6, 10)
	got := tr.Trim(history, 25)
	if len(got) == 0 {
		t.Fatal("NewKeepRecent Trim produced empty result")
	}
	var size int
	for _, m := range got {
		size += trim.SizeOf(m)
	}
	if size > 25 {
		t.Fatalf("NewKeepRecent Trim size %d exceeds budget", size)
	}
}
