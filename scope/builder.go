package scope

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNilExecutor indicates that Env has no model executor.
	ErrNilExecutor = errors.New("scope: nil executor")
	// ErrInvalidBuilder indicates an invalid builder receiver.
	ErrInvalidBuilder = errors.New("scope: invalid builder")
)

// Builder constructs isolated Scope instances.
type Builder struct {
	env   Env
	input Input
	meta  Meta
}

// NewBuilder creates a Scope builder.
func NewBuilder() *Builder { return &Builder{} }

// Env sets the run capabilities.
func (b *Builder) Env(env Env) *Builder {
	if b != nil {
		b.env = env
	}
	return b
}

// Input sets the run input.
func (b *Builder) Input(input Input) *Builder {
	if b != nil {
		b.input = cloneInput(input)
	}
	return b
}

// Meta sets optional run identity fields.
func (b *Builder) Meta(meta Meta) *Builder {
	if b != nil {
		b.meta = meta
	}
	return b
}

// Build validates the configuration and returns a new independent Scope.
func (b *Builder) Build() (*Scope, error) {
	if b == nil {
		return nil, ErrInvalidBuilder
	}
	if b.env.Executor == nil {
		return nil, ErrNilExecutor
	}
	meta := b.meta
	if meta.RunID == "" {
		id, err := newRunID()
		if err != nil {
			return nil, fmt.Errorf("scope: generate run id: %w", err)
		}
		meta.RunID = id
	}
	if meta.CreatedAt.IsZero() {
		meta.CreatedAt = time.Now()
	}
	input := cloneInput(b.input)
	if meta.SessionID == "" && (input.SessionKey.Scope != "" || input.SessionKey.ID != "") {
		meta.SessionID = input.SessionKey.Scope + "/" + input.SessionKey.ID
	}
	return &Scope{env: b.env, input: input, meta: meta}, nil
}

func newRunID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
