package agent

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/model"
	"github.com/JIAOZAI1/agent-go/session"
	"github.com/JIAOZAI1/agent-go/trim"
)

// capturingExecutor records the request messages it sees and returns a single
// stop turn so a Run completes after one model call.
type capturingExecutor struct {
	seen [][]message.Message
}

func (c *capturingExecutor) Model() model.Ref { return model.Ref{ProviderID: "test", ModelID: "test"} }

func (c *capturingExecutor) Generate(_ context.Context, request model.Request) (model.Stream, error) {
	c.seen = append(c.seen, request.Messages)
	return &oneShotStream{text: "ok"}, nil
}

type oneShotStream struct {
	text string
}

func (s *oneShotStream) Recv(context.Context) (model.Event, error) {
	if s.text == "" {
		return model.Event{}, io.EOF
	}
	text := s.text
	s.text = ""
	return model.Event{Delta: text, FinishReason: model.FinishStop}, nil
}

func (s *oneShotStream) Close() error { return nil }

// fillHistory stores count alternating user/assistant turns of `size` bytes
// each at increasing revisions.
func fillHistory(t *testing.T, store *session.MemoryStore, key session.Key, count, size int) {
	t.Helper()
	pad := strings.Repeat("x", size)
	var revision session.Revision
	for index := 0; index < count; index++ {
		next, err := store.Append(context.Background(), key, revision, []message.Message{
			message.Text(message.RoleUser, pad),
			message.Text(message.RoleAssistant, pad),
		})
		if err != nil {
			t.Fatalf("fillHistory Append: %v", err)
		}
		revision = next
	}
}

func TestToolLoopTrimsLoadedHistory(t *testing.T) {
	executor := &capturingExecutor{}
	store := session.NewMemoryStore()
	key := session.Key{Scope: "trim", ID: "1"}
	fillHistory(t, store, key, 100, 20)

	loop, err := NewToolLoopAgent(ToolLoopOptions{
		Executor:   executor,
		Store:      store,
		Trimmer:    trim.NewKeepRecent(),
		TrimBudget: 200,
	})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}

	if _, err := loop.Run(context.Background(), RunRequest{
		SessionKey: key,
		Input:      message.Text(message.RoleUser, "hi"),
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if len(executor.seen) != 1 {
		t.Fatalf("executor.Generate calls = %d, want 1", len(executor.seen))
	}
	// The request seen = trimmed loaded history + current input.
	request := executor.seen[0]
	var loadedSize int
	for index := 0; index < len(request)-1; index++ {
		loadedSize += trim.SizeOf(request[index])
	}
	if loadedSize > 200 {
		t.Fatalf("loaded history budget %d exceeds TrimBudget 200", loadedSize)
	}
	last := request[len(request)-1]
	if last.Role != message.RoleUser || last.Content[0].Text != "hi" {
		t.Fatalf("final message = %+v, want the current user input 'hi'", last)
	}
}

func TestToolLoopNoTrimmerLeavesHistoryUnchanged(t *testing.T) {
	executor := &capturingExecutor{}
	store := session.NewMemoryStore()
	key := session.Key{Scope: "trim", ID: "2"}
	fillHistory(t, store, key, 20, 0)

	loop, err := NewToolLoopAgent(ToolLoopOptions{Executor: executor, Store: store})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}
	if _, err := loop.Run(context.Background(), RunRequest{
		SessionKey: key,
		Input:      message.Text(message.RoleUser, "hi"),
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Without a Trimmer all 40 stored messages plus the current input survive.
	if got := len(executor.seen[0]); got != 41 {
		t.Fatalf("request messages without trimmer = %d, want 41 (40 stored + input)", got)
	}
}

func TestToolLoopTrimmerUsesDefaultFallbackBudget(t *testing.T) {
	executor := &capturingExecutor{}
	store := session.NewMemoryStore()
	key := session.Key{Scope: "trim", ID: "3"}
	fillHistory(t, store, key, 200, 300) // stored far above the fallback budget

	loop, err := NewToolLoopAgent(ToolLoopOptions{
		Executor: executor,
		Store:    store,
		Trimmer:  trim.NewKeepRecent(), // no TrimBudget -> default fallback
	})
	if err != nil {
		t.Fatalf("NewToolLoopAgent() error = %v", err)
	}
	if _, err := loop.Run(context.Background(), RunRequest{
		SessionKey: key,
		Input:      message.Text(message.RoleUser, "hi"),
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	request := executor.seen[0]
	// With a 200*600=120000-byte stored history and an 8192 fallback budget, the
	// loaded portion must have been trimmed well below the stored size while the
	// current input stays present.
	var loadedSize int
	for index := 0; index < len(request)-1; index++ {
		loadedSize += trim.SizeOf(request[index])
	}
	if loadedSize > 8192 {
		t.Fatalf("loaded history budget %d exceeds fallback 8192", loadedSize)
	}
	if len(request) == 401 {
		t.Fatalf("request holds the full 400 stored messages; trimming did not run")
	}
}
