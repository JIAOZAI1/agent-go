package model

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/JIAOZAI1/agent-go/message"
)

type testStream struct {
	events   []Event
	index    int
	closed   bool
	closeErr error
}

func (s *testStream) Recv(context.Context) (Event, error) {
	if s.index == len(s.events) {
		return Event{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *testStream) Close() error {
	s.closed = true
	return s.closeErr
}

func TestCollect(t *testing.T) {
	stream := &testStream{
		events: []Event{
			{Delta: "hello "},
			{Delta: "world", Usage: Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}},
			{FinishReason: FinishStop},
		},
	}

	got, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if !stream.closed {
		t.Fatal("Collect() did not close the stream")
	}
	if got.Message.Role != message.RoleAssistant || len(got.Message.Content) != 1 || got.Message.Content[0].Text != "hello world" {
		t.Fatalf("Collect() message = %+v, want assistant hello world", got.Message)
	}
	if got.Usage.TotalTokens != 5 || got.FinishReason != FinishStop {
		t.Fatalf("Collect() metadata = %+v, want usage and finish reason", got)
	}
}

func TestCollectReturnsReceiveErrorAndCloses(t *testing.T) {
	receiveErr := errors.New("receive failed")
	stream := &errorStream{err: receiveErr}

	_, err := Collect(context.Background(), stream)
	if !errors.Is(err, receiveErr) {
		t.Fatalf("Collect() error = %v, want %v", err, receiveErr)
	}
	if !stream.closed {
		t.Fatal("Collect() did not close the stream after receive error")
	}
}

type errorStream struct {
	err    error
	closed bool
}

func (s *errorStream) Recv(context.Context) (Event, error) { return Event{}, s.err }

func (s *errorStream) Close() error {
	s.closed = true
	return nil
}
