package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/session"
	sqlitestore "github.com/JIAOZAI1/agent-go/session/sqlite"
)

func openMemory(t *testing.T) *sqlitestore.Store {
	t.Helper()
	// An in-memory SQLite database is private to each pooled connection, so pin
	// MaxOpenConns to 1 to keep the schema and data on one logical database.
	store, err := sqlitestore.Open(sqlitestore.Config{DSN: ":memory:", MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// openFile opens a fresh temp-file store shared by a real database/sql pool,
// which is the production shape and supports genuinely concurrent connections.
func openFile(t *testing.T) *sqlitestore.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := sqlitestore.Open(sqlitestore.Config{DSN: "file:" + filepath.Join(dir, "agent.db") + "?_pragma=busy_timeout(5000)"})
	if err != nil {
		t.Fatalf("Open(file) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreAbsentConversation(t *testing.T) {
	store := openMemory(t)
	got, err := store.Load(context.Background(), session.Key{Scope: "s", ID: "none"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Revision != 0 || got.Messages != nil {
		t.Fatalf("Load(absent) = %+v, want zero revision and nil messages", got)
	}
}

func TestStoreAppendLoadRoundtrip(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "tenant", ID: "conv-1"}

	rev1, err := store.Append(context.Background(), key, 0, []message.Message{
		message.Text(message.RoleUser, "hello"),
	})
	if err != nil {
		t.Fatalf("Append#1 error = %v", err)
	}
	if rev1 != 1 {
		t.Fatalf("Append#1 revision = %d, want 1", rev1)
	}

	rev2, err := store.Append(context.Background(), key, rev1, []message.Message{
		message.Text(message.RoleAssistant, "hi"),
	})
	if err != nil {
		t.Fatalf("Append#2 error = %v", err)
	}
	if rev2 != 2 {
		t.Fatalf("Append#2 revision = %d, want 2", rev2)
	}

	snap, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if snap.Revision != rev2 {
		t.Fatalf("Load() revision = %d, want %d", snap.Revision, rev2)
	}
	if len(snap.Messages) != 2 {
		t.Fatalf("Load() has %d messages, want 2", len(snap.Messages))
	}
	if snap.Messages[0].Role != message.RoleUser || snap.Messages[1].Role != message.RoleAssistant {
		t.Fatalf("message roles out of order: %+v", snap.Messages)
	}
}

func TestStoreToolCallRoundtrip(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "tenant", ID: "tools"}
	assistant := message.Message{
		Role: message.RoleAssistant,
		Content: []message.ContentBlock{{
			Kind:     message.ContentToolCall,
			ToolCall: &message.ToolCall{ID: "c1", Name: "get_weather", Arguments: []byte(`{"city":"sf"}`)},
		}},
	}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{assistant}); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	snap, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	call := snap.Messages[0].Content[0].ToolCall
	if call == nil || call.Name != "get_weather" || string(call.Arguments) != `{"city":"sf"}` {
		t.Fatalf("tool call not roundtripped: %+v", snap.Messages[0].Content[0])
	}
}

func TestStoreConflictOnStaleRevision(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "s", ID: "c"}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "a")}); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	// Append with a stale expected revision must conflict.
	_, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "b")})
	if !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Append(stale) error = %v, want ErrConflict", err)
	}
	// The conflicting append must not have corrupted state.
	snap, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	if len(snap.Messages) != 1 || snap.Messages[0].Content[0].Text != "a" {
		t.Fatalf("state corrupted after conflict: %+v", snap.Messages)
	}
}

func TestStoreAppendNewToExistingConflicts(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "s", ID: "c2"}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "a")}); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	// expected: 0 implies "create new", but it already exists now.
	_, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "b")})
	if !errors.Is(err, session.ErrConflict) {
		t.Fatalf("Append(create existing) error = %v, want ErrConflict", err)
	}
}

func TestStoreValidationErrors(t *testing.T) {
	store := openMemory(t)
	user := message.Text(message.RoleUser, "x")

	tests := []struct {
		name    string
		key     session.Key
		msgs    []message.Message
		want    error
		because string
	}{
		{name: "nil context", key: session.Key{Scope: "s", ID: "c"}, msgs: []message.Message{user}, because: "nil-ctx load"},
		{name: "empty id", key: session.Key{Scope: "s"}, msgs: []message.Message{user}},
		{name: "empty scope", key: session.Key{ID: "c"}, msgs: []message.Message{user}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "nil context" {
				_, err := store.Load(nil, test.key) //nolint:staticcheck
				if !errors.Is(err, session.ErrInvalidContext) {
					t.Fatalf("Load(nil ctx) error = %v, want ErrInvalidContext", err)
				}
				return
			}
			if _, err := store.Load(context.Background(), test.key); !errors.Is(err, session.ErrInvalidKey) {
				t.Fatalf("Load(invalid key) error = %v, want ErrInvalidKey", err)
			}
		})
	}

	if _, err := store.Append(context.Background(), session.Key{Scope: "s", ID: "c"}, 0, nil); !errors.Is(err, session.ErrInvalidMessages) {
		t.Fatalf("Append(empty batch) error = %v, want ErrInvalidMessages", err)
	}
}

func TestStoreLoadReturnsIsolatedSnapshot(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "s", ID: "iso"}
	if _, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "a")}); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	snap, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}
	// Mutate the returned slice; it must not affect the stored history.
	snap.Messages[0].Content[0].Text = "corrupted"
	again, err := store.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load#2 error = %v", err)
	}
	if again.Messages[0].Content[0].Text != "a" {
		t.Fatalf("stored history mutated via snapshot: %q", again.Messages[0].Content[0].Text)
	}
}

func TestStoreConcurrentDifferentKeys(t *testing.T) {
	store := openFile(t)
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(index int) {
			defer wg.Done()
			key := session.Key{Scope: "par", ID: string(rune('a' + index))}
			if _, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "hi")}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Append error = %v", err)
	}
	// Every key should now have exactly one message at revision 1.
	for i := 0; i < n; i++ {
		key := session.Key{Scope: "par", ID: string(rune('a' + i))}
		snap, err := store.Load(context.Background(), key)
		if err != nil {
			t.Fatalf("Load error = %v", err)
		}
		if snap.Revision != 1 || len(snap.Messages) != 1 {
			t.Fatalf("key %s state = %d rev/%d msgs, want 1/1", key.ID, snap.Revision, len(snap.Messages))
		}
	}
}

func TestStoreConcurrentSameKeySingleWinner(t *testing.T) {
	store := openFile(t)
	key := session.Key{Scope: "par", ID: "same"}
	const writers = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	first := make(chan struct{})
	errs := make(chan error, writers)
	// All goroutines attempt to create the same new conversation concurrently.
	for i := 0; i < writers; i++ {
		go func(index int) {
			defer wg.Done()
			if index == 0 {
				close(first)
			} else {
				<-first
			}
			_, err := store.Append(context.Background(), key, 0, []message.Message{message.Text(message.RoleUser, "who")})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	successes, conflicts := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, session.ErrConflict):
			conflicts++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent creators = %d, want exactly 1", successes)
	}
	_ = conflicts
}

func TestStoreCancelledContext(t *testing.T) {
	store := openMemory(t)
	key := session.Key{Scope: "s", ID: "cancel"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(ctx, key); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load(cancelled) error = %v, want context.Canceled", err)
	}
	if _, err := store.Append(ctx, key, 0, []message.Message{message.Text(message.RoleUser, "x")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(cancelled) error = %v, want context.Canceled", err)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.db")
	cfg := sqlitestore.Config{DSN: "file:" + path}

	store1, err := sqlitestore.Open(cfg)
	if err != nil {
		t.Fatalf("Open#1 error = %v", err)
	}
	key := session.Key{Scope: "persist", ID: "conv"}
	if _, err := store1.Append(context.Background(), key, 0, []message.Message{
		message.Text(message.RoleUser, "hello"),
	}); err != nil {
		t.Fatalf("Append error = %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close#1 error = %v", err)
	}

	// Simulate a process restart: reopen the same DSN and load the history.
	store2, err := sqlitestore.Open(cfg)
	if err != nil {
		t.Fatalf("Open#2 error = %v", err)
	}
	defer store2.Close()
	snap, err := store2.Load(context.Background(), key)
	if err != nil {
		t.Fatalf("Load#2 error = %v", err)
	}
	if snap.Revision != 1 || len(snap.Messages) != 1 || snap.Messages[0].Content[0].Text != "hello" {
		t.Fatalf("persisted snapshot = %+v, want revision 1 with 'hello'", snap)
	}
}

func TestStoreClosedReportsError(t *testing.T) {
	store := openMemory(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if _, err := store.Load(context.Background(), session.Key{Scope: "s", ID: "c"}); err == nil {
		t.Fatal("Load on closed store returned nil error, want an error")
	}
}
