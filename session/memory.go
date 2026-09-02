package session

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/JIAOZAI1/agent-go/message"
)

// MemoryStore stores conversation history in process memory. Its zero value is
// ready for use and data is lost when the process exits.
type MemoryStore struct {
	mu    sync.RWMutex
	byKey map[Key]Snapshot
}

// NewMemoryStore creates an empty in-memory Store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byKey: make(map[Key]Snapshot)}
}

// Load returns an isolated snapshot. An absent conversation has revision zero.
func (s *MemoryStore) Load(ctx context.Context, key Key) (Snapshot, error) {
	if err := validateContext(ctx); err != nil {
		return Snapshot{}, err
	}
	if err := validateKey(key); err != nil {
		return Snapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	snapshot, ok := s.byKey[key]
	if !ok {
		return Snapshot{}, nil
	}
	snapshot.Messages = message.CloneSlice(snapshot.Messages)
	return snapshot, nil
}

// Append atomically appends messages when expected matches the current revision.
func (s *MemoryStore) Append(ctx context.Context, key Key, expected Revision, messages []message.Message) (Revision, error) {
	if err := validateContext(ctx); err != nil {
		return 0, err
	}
	if err := validateKey(key); err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return 0, ErrInvalidMessages
	}
	cloned := message.CloneSlice(messages)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if s.byKey == nil {
		s.byKey = make(map[Key]Snapshot)
	}
	current := s.byKey[key]
	if current.Revision != expected {
		return 0, fmt.Errorf("%w: expected revision %d", ErrConflict, expected)
	}
	if current.Revision == Revision(math.MaxUint64) {
		return 0, ErrRevisionExhausted
	}
	combined := make([]message.Message, 0, len(current.Messages)+len(cloned))
	combined = append(combined, message.CloneSlice(current.Messages)...)
	combined = append(combined, cloned...)
	newRevision := current.Revision + 1
	s.byKey[key] = Snapshot{Revision: newRevision, Messages: combined}
	return newRevision, nil
}
