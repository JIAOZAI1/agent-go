package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrInvalidStrategyName       = errors.New("agent: invalid strategy name")
	ErrStrategyAlreadyRegistered = errors.New("agent: strategy already registered")
	ErrStrategyNotFound          = errors.New("agent: strategy not found")
	ErrStrategyFactoryFrozen     = errors.New("agent: strategy factory is frozen")
)

// StrategyFactory registers reusable strategies and selects one for a request.
type StrategyFactory interface {
	Register(string, Strategy) error
	Default(string) error
	Freeze() error
	Select(context.Context, Request) (Strategy, error)
}

// DefaultStrategyFactory selects strategies by Request.Strategy.
type DefaultStrategyFactory struct {
	mu          sync.RWMutex
	strategies  map[string]Strategy
	defaultName string
	frozen      bool
}

// NewDefaultStrategyFactory creates an empty mutable factory.
func NewDefaultStrategyFactory() *DefaultStrategyFactory {
	return &DefaultStrategyFactory{strategies: make(map[string]Strategy)}
}

// Register adds a strategy instance under a unique name.
func (f *DefaultStrategyFactory) Register(name string, strategy Strategy) error {
	if f == nil || name == "" {
		return ErrInvalidStrategyName
	}
	if strategy == nil {
		return ErrNilStrategy
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frozen {
		return ErrStrategyFactoryFrozen
	}
	if _, exists := f.strategies[name]; exists {
		return fmt.Errorf("%w: %s", ErrStrategyAlreadyRegistered, name)
	}
	if f.strategies == nil {
		f.strategies = make(map[string]Strategy)
	}
	f.strategies[name] = strategy
	return nil
}

// Default configures the strategy selected for an empty request name.
func (f *DefaultStrategyFactory) Default(name string) error {
	if f == nil || name == "" {
		return ErrInvalidStrategyName
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frozen {
		return ErrStrategyFactoryFrozen
	}
	if _, exists := f.strategies[name]; !exists {
		return fmt.Errorf("%w: %s", ErrStrategyNotFound, name)
	}
	f.defaultName = name
	return nil
}

// Freeze makes registration immutable. It is idempotent.
func (f *DefaultStrategyFactory) Freeze() error {
	if f == nil {
		return ErrNilStrategyFactory
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frozen = true
	return nil
}

// Select returns the requested or default strategy instance.
func (f *DefaultStrategyFactory) Select(ctx context.Context, request Request) (Strategy, error) {
	if ctx == nil {
		return nil, ErrInvalidContext
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, ErrNilStrategyFactory
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	name := request.Strategy
	if name == "" {
		name = f.defaultName
	}
	strategy, exists := f.strategies[name]
	if !exists || name == "" {
		return nil, fmt.Errorf("%w: %s", ErrStrategyNotFound, name)
	}
	return strategy, nil
}
