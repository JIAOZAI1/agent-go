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

// SystemText creates a system text message.
func SystemText(value string) Message {
	return Text(RoleSystem, value)
}

// UserText creates a user text message.
func UserText(value string) Message {
	return Text(RoleUser, value)
}

// Assistant creates an assistant text message.
func Assistant(value string) Message {
	return Text(RoleAssistant, value)
}

// AssistantText creates an assistant text message.
func AssistantText(value string) Message {
	return Assistant(value)
}

// ToolResult creates a successful tool response message.
func ToolResult(callID, value string) Message {
	result := Text(RoleTool, value)
	result.ToolCallID = callID
	return result
}

// ToolError creates a failed tool response message.
func ToolError(callID, value string) Message {
	result := ToolResult(callID, value)
	result.IsError = true
	return result
}

// New creates a message from independent copies of blocks.
func New(role Role, blocks ...ContentBlock) Message {
	content := make([]ContentBlock, len(blocks))
	for index, block := range blocks {
		content[index] = cloneContentBlock(block)
	}
	return Message{Role: role, Content: content}
}

// TextBlock creates a text content block.
func TextBlock(value string) ContentBlock {
	return ContentBlock{Kind: ContentText, Text: value}
}

// ImageURL creates an image content block backed by a URL.
func ImageURL(url, mimeType string) ContentBlock {
	return ContentBlock{Kind: ContentImage, URL: url, MIMEType: mimeType}
}

// ImageData creates an image content block backed by encoded data.
func ImageData(data, mimeType string) ContentBlock {
	return ContentBlock{Kind: ContentImage, Data: data, MIMEType: mimeType}
}

// ToolCallBlock creates a tool-call content block with independent arguments.
func ToolCallBlock(call ToolCall) ContentBlock {
	call.Arguments = bytes.Clone(call.Arguments)
	return ContentBlock{Kind: ContentToolCall, ToolCall: &call}
}

// ToolCalls returns independent copies of the tool calls carried by value's
// ContentToolCall blocks, in content order. It returns nil if value has no
// tool calls.
func ToolCalls(value Message) []ToolCall {
	var calls []ToolCall
	for _, block := range value.Content {
		if block.Kind == ContentToolCall && block.ToolCall != nil {
			call := *block.ToolCall
			call.Arguments = bytes.Clone(call.Arguments)
			calls = append(calls, call)
		}
	}
	return calls
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
