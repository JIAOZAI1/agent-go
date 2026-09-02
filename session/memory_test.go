package session_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/session"
)

func TestMemoryStoreAppendLoadAndConflict(t *testing.T) {
	store := &session.MemoryStore{}
	key := session.Key{Scope: "tenant", ID: "conversation"}

	snapshot, err := store.Load(context.Background(), key)
	if err != nil || snapshot.Revision != 0 || len(snapshot.Messages) != 0 {
		t.Fatalf("initial Load() = %+v, %v", snapshot, err)
	}
	first := message.Text(message.RoleUser, "first")
	revision, err := store.Append(context.Background(), key, 0, []message.Message{first})
	if err != nil || revision != 1 {
		t.Fatalf("first Append() revision = %d, %v", revision, err)
	}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "stale")}); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("stale Append() error = %v", err)
	}
	snapshot, err = store.Load(context.Background(), key)
	if err != nil || len(snapshot.Messages) != 1 || snapshot.Messages[0].Content[0].Text != "first" {
		t.Fatalf("final Load() = %+v, %v", snapshot, err)
	}
}

func TestMemoryStoreCopiesMessages(t *testing.T) {
	store := session.NewMemoryStore()
	key := session.Key{Scope: "tenant", ID: "copy"}
	arguments := []byte(`{"value":1}`)
	input := message.Message{Role: message.RoleAssistant, Content: []message.ContentBlock{{Kind: message.ContentToolCall, ToolCall: &message.ToolCall{Arguments: arguments}}}}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{input}); err != nil {
		t.Fatal(err)
	}
	input.Content[0].ToolCall.Arguments[0] = '['
	snapshot, err := store.Load(context.Background(), key)
	if err != nil || string(snapshot.Messages[0].Content[0].ToolCall.Arguments) != `{"value":1}` {
		t.Fatalf("Load() = %+v, %v", snapshot, err)
	}
	snapshot.Messages[0].Content[0].ToolCall.Arguments[0] = '['
	again, err := store.Load(context.Background(), key)
	if err != nil || string(again.Messages[0].Content[0].ToolCall.Arguments) != `{"value":1}` {
		t.Fatalf("second Load() = %+v, %v", again, err)
	}
}

func TestMemoryStoreConcurrentAppend(t *testing.T) {
	store := session.NewMemoryStore()
	key := session.Key{Scope: "tenant", ID: "concurrent"}
	const workers = 8
	var wg sync.WaitGroup
	var successes, conflicts int
	var mu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "message")})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				successes++
			case errors.Is(err, session.ErrConflict):
				conflicts++
			default:
				t.Errorf("Append() error = %v", err)
			}
		}()
	}
	wg.Wait()
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("successes = %d, conflicts = %d", successes, conflicts)
	}
}
