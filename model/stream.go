package model

import (
	"context"
	"errors"
	"io"

	"github.com/JIAOZAI1/agent-go/message"
)

// Stream provides events produced by one model generation.
type Stream interface {
	Recv(ctx context.Context) (Event, error)
	Close() error
}

// Event is one incremental result from a model generation.
type Event struct {
	Delta        string
	Usage        Usage
	FinishReason FinishReason
}

// Collect consumes a stream and assembles its complete response.
func Collect(ctx context.Context, stream Stream) (response Response, err error) {
	if ctx == nil {
		return Response{}, errors.New("collect model stream: nil context")
	}
	if stream == nil {
		return Response{}, errors.New("collect model stream: nil stream")
	}

	defer func() {
		closeErr := stream.Close()
		if err == nil && closeErr != nil {
			err = errors.Join(errors.New("close model stream"), closeErr)
		}
	}()

	content := make([]byte, 0)
	for {
		event, recvErr := stream.Recv(ctx)
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				response.Message = message.Text(message.RoleAssistant, string(content))
				return response, nil
			}
			return Response{}, recvErr
		}

		content = append(content, event.Delta...)
		if event.Usage.TotalTokens != 0 {
			response.Usage = event.Usage
		}
		if event.FinishReason != "" {
			response.FinishReason = event.FinishReason
		}
	}
}
