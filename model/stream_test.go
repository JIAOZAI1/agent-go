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

func TestCollectAssemblesTextAndToolCalls(t *testing.T) {
	stream := &testStream{
		events: []Event{
			{Delta: "checking weather"},
			{ToolCall: &message.ToolCall{ID: "1", Name: "get_weather", Arguments: []byte(`{"city":"sf"}`)}},
			{ToolCall: &message.ToolCall{ID: "2", Name: "get_time", Arguments: []byte(`{}`)}},
			{FinishReason: FinishToolCall},
		},
	}

	got, err := Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got.FinishReason != FinishToolCall {
		t.Fatalf("Collect() FinishReason = %v, want %v", got.FinishReason, FinishToolCall)
	}
	if len(got.Message.Content) != 3 {
		t.Fatalf("Collect() message content = %+v, want 1 text block + 2 tool call blocks", got.Message.Content)
	}
	if got.Message.Content[0].Kind != message.ContentText || got.Message.Content[0].Text != "checking weather" {
		t.Errorf("Collect() text block = %+v", got.Message.Content[0])
	}
	calls := message.ToolCalls(got.Message)
	if len(calls) != 2 || calls[0].Name != "get_weather" || calls[1].Name != "get_time" {
		t.Errorf("message.ToolCalls(Collect() message) = %+v, want get_weather then get_time", calls)
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
