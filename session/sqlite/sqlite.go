// Package sqlite implements the session.Store contract against a SQLite
// database through the pure-Go (CGO-free) modernc.org/sqlite driver.
//
// It deliberately offers no caching layer and keeps no in-process session
// state: every Load and Append reads and writes the database directly, so data
// survives process restarts when DSN points at a durable file (e.g.
// "file:agent.db?_pragma=busy_timeout(5000)").
//
// Storage model: each conversation is stored as a single row whose payload is
// the serialized full history (JSON of []message.Message). Append reads the
// current payload and rewrites the whole row inside one transaction, guarded by
// the conversation's optimistic-concurrency revision. SQLite serializes
// writers, so concurrent Appends to the same conversation with a stale expected
// revision fail with session.ErrConflict, matching the rest of the library.
//
// Known trade-off: rewriting the whole-history blob on every Append is O(N) per
// run and O(N^2) over a long conversation. It is inherited from the "whole
// history per row" storage model chosen for simplicity. A row-per-message or
// append-log backend is a clean future enhancement that keeps the same
// session.Store contract.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/session"
	_ "modernc.org/sqlite" // register the "sqlite" database/sql driver
)

// Config configures a persistent SQLite-backed session.Store.
type Config struct {
	// DSN is a SQLite data-source name understood by the driver, e.g.
	// "file:agent.db?_pragma=busy_timeout(5000)". Use ":memory:" or a file with
	// "?cache=shared" for a throwaway database (typical in tests).
	DSN string
	// MaxOpenConns overrides the open-connection limit on the underlying
	// database/sql pool. 0 leaves the driver default.
	MaxOpenConns int
}

// schema creates the single conversation table on first open. updated_at is
// informational only.
const schema = `
CREATE TABLE IF NOT EXISTS conversations (
    scope      TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    revision   INTEGER NOT NULL,
    payload    BLOB    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (scope, id)
);`

// Store is a thread-safe, persistent session.Store backed by SQLite. Its zero
// value is not usable; construct it with Open. It is safe for concurrent use
// across goroutines.
type Store struct {
	db *sql.DB
}

var _ session.Store = (*Store)(nil)

// Open opens (or creates) the SQLite database at cfg.DSN, ensures the schema
// exists, and returns a ready Store. The caller owns the lifecycle and should
// call Close when done.
func Open(cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("session/sqlite: empty DSN")
	}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("session/sqlite: open %q: %w", cfg.DSN, err)
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session/sqlite: ping %q: %w", cfg.DSN, err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("session/sqlite: create schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying connection pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Load returns an isolated snapshot of the conversation for key. An absent
// conversation has revision zero and nil messages.
func (s *Store) Load(ctx context.Context, key session.Key) (session.Snapshot, error) {
	if err := validate(s, ctx, key); err != nil {
		return session.Snapshot{}, err
	}
	var revision uint64
	var payload []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT revision, payload FROM conversations WHERE scope = ? AND id = ?`,
		key.Scope, key.ID,
	).Scan(&revision, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Snapshot{}, nil
	}
	if err != nil {
		return session.Snapshot{}, fmt.Errorf("session/sqlite: load: %w", err)
	}
	msgs, err := decodePayload(payload)
	if err != nil {
		return session.Snapshot{}, err
	}
	return session.Snapshot{Revision: session.Revision(revision), Messages: msgs}, nil
}

// Append atomically appends messages to key when expected equals the stored
// revision, returning the new revision. A stale expected returns
// session.ErrConflict; an absent conversation is treated as revision 0 and
// created on first successful Append (expected must be 0).
func (s *Store) Append(ctx context.Context, key session.Key, expected session.Revision, messages []message.Message) (session.Revision, error) {
	if err := validate(s, ctx, key); err != nil {
		return 0, err
	}
	if len(messages) == 0 {
		return 0, session.ErrInvalidMessages
	}
	if expected == session.Revision(math.MaxUint64) {
		return 0, session.ErrRevisionExhausted
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once Commit succeeds

	// Isolate the new conversation from empty-expected writers by keying the
	// CAS below on `expected`, so a concurrent first-writer that created the row
	// after we began will trip the guard.
	if expected == 0 {
		return s.appendNew(ctx, tx, key, messages)
	}
	return s.appendExisting(ctx, tx, key, expected, messages)
}

// appendNew treats the conversation as absent and creates it. A concurrent
// creation is detected by the row's unique key and reported as a conflict.
func (s *Store) appendNew(ctx context.Context, tx *sql.Tx, key session.Key, messages []message.Message) (session.Revision, error) {
	payload, err := json.Marshal(message.CloneSlice(messages))
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: encode: %w", err)
	}
	newRev := session.Revision(1)
	_, err = tx.ExecContext(ctx,
		`INSERT INTO conversations (scope, id, revision, payload, updated_at) VALUES (?, ?, ?, ?, ?)`,
		key.Scope, key.ID, uint64(newRev), payload, time.Now().UnixMilli(),
	)
	if err != nil {
		// The only constraint this schema can violate is the (scope,id) primary
		// key: a concurrent writer already created the conversation between our
		// guard check and this insert. Report it as an optimistic conflict.
		var exists int
		_ = tx.QueryRowContext(ctx, `SELECT 1 FROM conversations WHERE scope = ? AND id = ?`, key.Scope, key.ID).Scan(&exists)
		if exists == 1 {
			return 0, fmt.Errorf("%w: conversation already exists", session.ErrConflict)
		}
		return 0, fmt.Errorf("session/sqlite: append: insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("session/sqlite: append: commit: %w", err)
	}
	return newRev, nil
}

// appendExisting loads the stored history inside the transaction, appends the
// new batch, and writes it back guarded by WHERE revision = expected. SQLite
// serializes writers so a stale expected yields zero affected rows -> conflict,
// which also prevents lost updates between concurrent writers.
func (s *Store) appendExisting(ctx context.Context, tx *sql.Tx, key session.Key, expected session.Revision, messages []message.Message) (session.Revision, error) {
	var payload []byte
	err := tx.QueryRowContext(ctx,
		`SELECT payload FROM conversations WHERE scope = ? AND id = ? AND revision = ?`,
		key.Scope, key.ID, uint64(expected),
	).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: expected revision %d not found", session.ErrConflict, expected)
	}
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: read history: %w", err)
	}
	history, err := decodePayload(payload)
	if err != nil {
		return 0, err
	}
	history = append(history, message.CloneSlice(messages)...)
	next, err := json.Marshal(history)
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: encode: %w", err)
	}
	newRev := session.Revision(expected + 1)
	result, err := tx.ExecContext(ctx,
		`UPDATE conversations SET revision = ?, payload = ?, updated_at = ?
		 WHERE scope = ? AND id = ? AND revision = ?`,
		uint64(newRev), next, time.Now().UnixMilli(), key.Scope, key.ID, uint64(expected),
	)
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: update: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("session/sqlite: append: rows: %w", err)
	}
	if affected != 1 {
		return 0, fmt.Errorf("%w: expected revision %d", session.ErrConflict, expected)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("session/sqlite: append: commit: %w", err)
	}
	return newRev, nil
}

// decodePayload parses a payload blob into an isolated message slice.
func decodePayload(payload []byte) ([]message.Message, error) {
	var msgs []message.Message
	if err := json.Unmarshal(payload, &msgs); err != nil {
		return nil, fmt.Errorf("session/sqlite: decode history: %w", err)
	}
	return message.CloneSlice(msgs), nil
}

// validate enforces the shared context/key guards and maps errors to the
// sentinel errors defined in the session package.
func validate(store *Store, ctx context.Context, key session.Key) error {
	if store == nil || store.db == nil {
		return errors.New("session/sqlite: store is closed")
	}
	if ctx == nil {
		return session.ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if key.Scope == "" || key.ID == "" {
		return session.ErrInvalidKey
	}
	return nil
}
