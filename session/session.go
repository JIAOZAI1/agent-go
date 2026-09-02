// Package session defines conversation-history storage contracts.
package session

import (
	"context"
	"errors"

	"github.com/JIAOZAI1/agent-go/message"
)

// Key identifies one conversation in an application-defined scope.
type Key struct {
	Scope string
	ID    string
}

// Revision is the optimistic-concurrency version of one conversation.
type Revision uint64

// Snapshot is one isolated conversation-history view.
type Snapshot struct {
	Revision Revision
	Messages []message.Message
}

// Store loads conversation history and atomically appends messages.
type Store interface {
	Load(context.Context, Key) (Snapshot, error)
	Append(context.Context, Key, Revision, []message.Message) (Revision, error)
}

var (
	// ErrInvalidContext indicates that an operation received a nil context.
	ErrInvalidContext = errors.New("session: invalid context")
	// ErrInvalidKey indicates that a key has an empty scope or ID.
	ErrInvalidKey = errors.New("session: invalid key")
	// ErrInvalidMessages indicates that an append batch has no messages.
	ErrInvalidMessages = errors.New("session: invalid messages")
	// ErrConflict indicates that the expected revision is stale.
	ErrConflict = errors.New("session: conflict")
	// ErrRevisionExhausted indicates that a revision cannot be incremented.
	ErrRevisionExhausted = errors.New("session: revision exhausted")
)

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContext
	}
	return ctx.Err()
}

func validateKey(key Key) error {
	if key.Scope == "" || key.ID == "" {
		return ErrInvalidKey
	}
	return nil
}

var _ Store = (*MemoryStore)(nil)
