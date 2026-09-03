package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/tool"
)

var (
	// ErrInvalidEvent indicates that an event is missing required identity data.
	ErrInvalidEvent = errors.New("agent: invalid event")
	// ErrDuplicateSubscription indicates that a subscription name is already registered.
	ErrDuplicateSubscription = errors.New("agent: duplicate subscription")
	// ErrInvalidSubscription indicates that a subscription handler or name is invalid.
	ErrInvalidSubscription = errors.New("agent: invalid subscription")
	// ErrSinkClosed indicates that a sink no longer accepts events.
	ErrSinkClosed = errors.New("agent: event sink is closed")
)

// EventSink receives events produced by an Agent run.
type EventSink interface {
	Publish(context.Context, Event) error
}

// EventHandler consumes events from one FanoutSink subscription.
type EventHandler func(context.Context, Event) error

// OverflowPolicy determines what happens when a subscriber queue is full.
type OverflowPolicy string

const (
	// OverflowBlock applies backpressure to Publish until the subscriber accepts the event.
	OverflowBlock OverflowPolicy = "block"
	// OverflowDropNewest drops a new event when the subscriber queue is full.
	OverflowDropNewest OverflowPolicy = "drop_newest"
)

// FanoutOptions configures a FanoutSink.
type FanoutOptions struct {
	QueueSize int
	Overflow  OverflowPolicy
	OnError   func(subscription string, err error)
}

// Subscription represents one independent event consumer.
type Subscription interface {
	Close() error
	Stats() SubscriptionStats
}

// SubscriptionStats reports delivery outcomes for one subscription.
type SubscriptionStats struct {
	Delivered uint64
	Dropped   uint64
	Errors    uint64
}

// FanoutSink delivers each event to independent subscriber queues.
type FanoutSink struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	options     FanoutOptions
	closed      bool
	wait        sync.WaitGroup
}

// NewFanoutSink creates a concurrent event fanout with bounded subscriber queues.
func NewFanoutSink(options FanoutOptions) *FanoutSink {
	if options.QueueSize <= 0 {
		options.QueueSize = 256
	}
	if options.Overflow == "" {
		options.Overflow = OverflowDropNewest
	}
	return &FanoutSink{options: options, subscribers: make(map[string]*subscriber)}
}

// Subscribe registers an event handler under a unique name.
func (s *FanoutSink) Subscribe(name string, handler EventHandler) (Subscription, error) {
	if s == nil || name == "" || handler == nil {
		return nil, ErrInvalidSubscription
	}
	if s.options.Overflow != OverflowBlock && s.options.Overflow != OverflowDropNewest {
		return nil, fmt.Errorf("%w: unsupported overflow policy %q", ErrInvalidSubscription, s.options.Overflow)
	}

	value := &subscriber{
		owner:   s,
		name:    name,
		handler: handler,
		events:  make(chan Event, s.options.QueueSize),
		done:    make(chan struct{}),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrSinkClosed
	}
	if _, exists := s.subscribers[name]; exists {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateSubscription, name)
	}
	s.subscribers[name] = value
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		value.consume(s.options.OnError)
	}()
	return value, nil
}

// Publish sends an event to every active subscriber.
func (s *FanoutSink) Publish(ctx context.Context, event Event) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidEvent)
	}
	if err := validateEvent(event); err != nil {
		return err
	}

	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return ErrSinkClosed
	}
	subscribers := make([]*subscriber, 0, len(s.subscribers))
	for _, value := range s.subscribers {
		subscribers = append(subscribers, value)
	}
	s.mu.RUnlock()

	for _, value := range subscribers {
		if err := value.publish(ctx, cloneEvent(event), s.options.Overflow); err != nil {
			return err
		}
	}
	return nil
}

// Close stops accepting events and drains all subscriber queues.
func (s *FanoutSink) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	subscribers := make([]*subscriber, 0, len(s.subscribers))
	for _, value := range s.subscribers {
		subscribers = append(subscribers, value)
	}
	s.mu.Unlock()

	for _, value := range subscribers {
		value.Close()
	}
	s.wait.Wait()
	return nil
}

type subscriber struct {
	owner   *FanoutSink
	name    string
	handler EventHandler
	events  chan Event
	done    chan struct{}
	closed  atomic.Bool
	stats   atomic.Uint64
	dropped atomic.Uint64
	errors  atomic.Uint64
}

func (s *subscriber) publish(ctx context.Context, event Event, policy OverflowPolicy) error {
	if s.closed.Load() {
		return nil
	}
	if policy == OverflowDropNewest {
		select {
		case <-s.done:
			return nil
		case s.events <- event:
		default:
			s.dropped.Add(1)
		}
		return nil
	}
	select {
	case s.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return nil
	}
}

func (s *subscriber) consume(onError func(string, error)) {
	for {
		select {
		case event := <-s.events:
			s.consumeEvent(onError, event)
		case <-s.done:
			for {
				select {
				case event := <-s.events:
					s.consumeEvent(onError, event)
				default:
					return
				}
			}
		}
	}
}

func (s *subscriber) consumeEvent(onError func(string, error), event Event) {
	if err := s.handler(context.Background(), event); err != nil {
		s.errors.Add(1)
		if onError != nil {
			onError(s.name, err)
		}
		return
	}
	s.stats.Add(1)
}

// Close removes the subscription input and lets the consumer drain queued events.
func (s *subscriber) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.done)
	s.owner.mu.Lock()
	delete(s.owner.subscribers, s.name)
	s.owner.mu.Unlock()
	return nil
}

// Stats returns a snapshot of delivery statistics.
func (s *subscriber) Stats() SubscriptionStats {
	return SubscriptionStats{
		Delivered: s.stats.Load(),
		Dropped:   s.dropped.Load(),
		Errors:    s.errors.Load(),
	}
}

func validateEvent(event Event) error {
	if event.RunID == "" || event.Sequence == 0 || event.Type == "" {
		return ErrInvalidEvent
	}
	return nil
}

func cloneEvent(value Event) Event {
	switch data := value.Data.(type) {
	case RunStarted:
		data.Input = cloneMessage(data.Input)
		value.Data = data
	case InputReceived:
		data.Message = cloneMessage(data.Message)
		value.Data = data
	case ModelGenerationFinished:
		data.Message = cloneMessage(data.Message)
		value.Data = data
	case ToolCallRequested:
		data.Call = cloneCall(data.Call)
		value.Data = data
	case ToolExecutionStarted:
		data.Call = cloneCall(data.Call)
		value.Data = data
	case ToolExecutionFinished:
		data.Call = cloneCall(data.Call)
		value.Data = data
	}
	return value
}

func cloneMessage(value message.Message) message.Message {
	return message.Clone(value)
}

func cloneCall(value tool.Call) tool.Call {
	value.Arguments = append([]byte(nil), value.Arguments...)
	return value
}

var _ EventSink = (*FanoutSink)(nil)
