// Package message defines provider-independent conversation messages.
package message

import "bytes"

// Role identifies the author or purpose of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ContentKind identifies the payload represented by a ContentBlock.
type ContentKind string

const (
	ContentText     ContentKind = "text"
	ContentImage    ContentKind = "image"
	ContentToolCall ContentKind = "tool_call"
)

// ContentBlock is one provider-independent content part.
type ContentBlock struct {
	Kind     ContentKind `json:"kind"`
	Text     string      `json:"text,omitempty"`
	MIMEType string      `json:"mimeType,omitempty"`
	URL      string      `json:"url,omitempty"`
	Data     string      `json:"data,omitempty"`
	ToolCall *ToolCall   `json:"toolCall,omitempty"`
}

// ToolCall is a model-requested function invocation.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments []byte `json:"arguments"`
}

// Message is replayable conversation data.
type Message struct {
	Role       Role           `json:"role"`
	Content    []ContentBlock `json:"content"`
	ToolCallID string         `json:"toolCallId,omitempty"`
	IsError    bool           `json:"isError,omitempty"`
}

// Text creates a text message with one ContentText block.
func Text(role Role, value string) Message {
	return Message{Role: role, Content: []ContentBlock{{Kind: ContentText, Text: value}}}
}

// Clone returns an independent copy of the message and all reference-backed fields.
func Clone(value Message) Message {
	content := value.Content
	value.Content = make([]ContentBlock, len(content))
	for index, block := range content {
		value.Content[index] = cloneContentBlock(block)
	}
	return value
}

// CloneSlice returns an independent copy of each message in values.
func CloneSlice(values []Message) []Message {
	if values == nil {
		return nil
	}
	cloned := make([]Message, len(values))
	for index, value := range values {
		cloned[index] = Clone(value)
	}
	return cloned
}

func cloneContentBlock(value ContentBlock) ContentBlock {
	if value.ToolCall == nil {
		return value
	}
	call := *value.ToolCall
	call.Arguments = bytes.Clone(call.Arguments)
	value.ToolCall = &call
	return value
}
