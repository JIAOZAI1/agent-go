package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/JIAOZAI1/agent-go/agent"
	"github.com/JIAOZAI1/agent-go/message"
	"github.com/JIAOZAI1/agent-go/tool"
)

func TestFanoutPublishesToSubscribersAndCopiesData(t *testing.T) {
	var wait sync.WaitGroup
	wait.Add(2)
	seen := make(chan string, 1)
	sink := agent.NewFanoutSink(agent.FanoutOptions{QueueSize: 4})
	first, err := sink.Subscribe("first", func(_ context.Context, event agent.Event) error {
		data := event.Data.(agent.ToolCallRequested)
		data.Call.Arguments[0] = 'X'
		wait.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sink.Subscribe("second", func(_ context.Context, event agent.Event) error {
		data := event.Data.(agent.ToolCallRequested)
		seen <- string(data.Call.Arguments)
		wait.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	event := agent.Event{
		RunID:    "run-1",
		Sequence: 1,
		Type:     agent.EventToolCallRequested,
		Data:     agent.ToolCallRequested{Call: tool.Call{Name: "search", Arguments: []byte(`{"q":"go"}`)}},
	}
	if err := sink.Publish(context.Background(), event); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	wait.Wait()
	if got := <-seen; got != `{"q":"go"}` {
		t.Fatalf("second subscriber arguments = %q, want original arguments", got)
	}
	if first.Stats().Delivered != 1 || second.Stats().Delivered != 1 {
		t.Fatalf("stats = (%+v, %+v), want one delivery each", first.Stats(), second.Stats())
	}
}

func TestFanoutDropsNewestWhenQueueIsFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sink := agent.NewFanoutSink(agent.FanoutOptions{QueueSize: 1, Overflow: agent.OverflowDropNewest})
	subscription, err := sink.Subscribe("slow", func(_ context.Context, _ agent.Event) error {
		select {
		case <-started:
		case <-time.After(time.Second):
			return errors.New("handler did not start")
		}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	newEvent := func(sequence uint64) agent.Event {
		return agent.Event{RunID: "run-1", Sequence: sequence, Type: agent.EventRunStarted, Data: agent.RunStarted{}}
	}
	close(started)
	if err := sink.Publish(context.Background(), newEvent(1)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Publish(context.Background(), newEvent(2)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Publish(context.Background(), newEvent(3)); err != nil {
		t.Fatal(err)
	}
	close(release)

	deadline := time.After(time.Second)
	for subscription.Stats().Delivered < 1 && subscription.Stats().Dropped < 1 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for subscriber statistics")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if subscription.Stats().Dropped == 0 {
		t.Fatalf("stats = %+v, want dropped events", subscription.Stats())
	}
}

func TestFanoutConsumerErrorsAreIsolated(t *testing.T) {
	received := make(chan struct{})
	reported := make(chan error, 1)
	sink := agent.NewFanoutSink(agent.FanoutOptions{
		QueueSize: 1,
		OnError: func(_ string, err error) {
			reported <- err
		},
	})
	failed, err := sink.Subscribe("failed", func(context.Context, agent.Event) error {
		return errors.New("consumer failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	working, err := sink.Subscribe("working", func(context.Context, agent.Event) error {
		close(received)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()

	event := agent.Event{RunID: "run-1", Sequence: 1, Type: agent.EventInputReceived, Data: agent.InputReceived{Message: message.Text(message.RoleUser, "hello")}}
	if err := sink.Publish(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("working subscriber did not receive event")
	}
	deadline := time.After(time.Second)
	for failed.Stats().Errors == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for consumer error")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	select {
	case err := <-reported:
		if err.Error() != "consumer failed" {
			t.Fatalf("reported error = %v, want consumer failed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reported consumer error")
	}
	if working.Stats().Delivered != 1 {
		t.Fatalf("working stats = %+v, want one delivery", working.Stats())
	}
}

func TestFanoutValidationAndClose(t *testing.T) {
	sink := agent.NewFanoutSink(agent.FanoutOptions{})
	if _, err := sink.Subscribe("", nil); !errors.Is(err, agent.ErrInvalidSubscription) {
		t.Fatalf("Subscribe() error = %v, want invalid subscription", err)
	}
	if err := sink.Publish(context.Background(), agent.Event{}); !errors.Is(err, agent.ErrInvalidEvent) {
		t.Fatalf("Publish() error = %v, want invalid event", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := sink.Publish(context.Background(), agent.Event{RunID: "run", Sequence: 1, Type: agent.EventRunStarted}); !errors.Is(err, agent.ErrSinkClosed) {
		t.Fatalf("Publish() after Close error = %v, want sink closed", err)
	}
}
