package agent

import (
	"context"

	"github.com/JIAOZAI1/agent-go/message"
)

// Agent processes an input message and returns an output message.
type Agent interface {
	Run(ctx context.Context, input message.Message) (message.Message, error)
}
