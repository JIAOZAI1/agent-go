package message

import "testing"

func TestTextConvenienceConstructors(t *testing.T) {
	tests := []struct {
		name  string
		build func(string) Message
		role  Role
	}{
		{name: "system", build: SystemText, role: RoleSystem},
		{name: "user", build: UserText, role: RoleUser},
		{name: "assistant", build: Assistant, role: RoleAssistant},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.build("hello")
			want := Text(test.role, "hello")
			if got.Role != want.Role || len(got.Content) != 1 || got.Content[0] != want.Content[0] {
				t.Fatalf("constructor result = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMessageConvenienceConstructors(t *testing.T) {
	assistant := AssistantText("done")
	if assistant.Role != RoleAssistant || assistant.Content[0].Text != "done" {
		t.Fatalf("AssistantText() = %+v", assistant)
	}

	result := ToolResult("call-1", "sunny")
	if result.Role != RoleTool || result.ToolCallID != "call-1" || result.IsError || result.Content[0].Text != "sunny" {
		t.Fatalf("ToolResult() = %+v", result)
	}

	failed := ToolError("call-2", "failed")
	if failed.Role != RoleTool || failed.ToolCallID != "call-2" || !failed.IsError || failed.Content[0].Text != "failed" {
		t.Fatalf("ToolError() = %+v", failed)
	}
}

func TestContentBlockConstructors(t *testing.T) {
	text := TextBlock("hello")
	if text.Kind != ContentText || text.Text != "hello" {
		t.Fatalf("TextBlock() = %+v", text)
	}
	url := ImageURL("https://example.com/image.png", "image/png")
	if url.Kind != ContentImage || url.URL == "" || url.MIMEType != "image/png" {
		t.Fatalf("ImageURL() = %+v", url)
	}
	data := ImageData("encoded", "image/jpeg")
	if data.Kind != ContentImage || data.Data != "encoded" || data.MIMEType != "image/jpeg" {
		t.Fatalf("ImageData() = %+v", data)
	}
}

func TestNewAndToolCallBlockCopyArguments(t *testing.T) {
	arguments := []byte(`{"city":"beijing"}`)
	block := ToolCallBlock(ToolCall{ID: "call-1", Name: "weather", Arguments: arguments})
	value := New(RoleAssistant, TextBlock("checking"), block)

	arguments[2] = 'X'
	block.ToolCall.Arguments[3] = 'Y'
	if got := string(value.Content[1].ToolCall.Arguments); got != `{"city":"beijing"}` {
		t.Fatalf("New() tool arguments = %q, want independent copy", got)
	}
	if value.Role != RoleAssistant || len(value.Content) != 2 {
		t.Fatalf("New() = %+v", value)
	}
}

func TestToolCallsReturnsBlocksInOrder(t *testing.T) {
	value := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Kind: ContentText, Text: "checking weather"},
			{Kind: ContentToolCall, ToolCall: &ToolCall{ID: "1", Name: "get_weather", Arguments: []byte(`{"city":"sf"}`)}},
			{Kind: ContentToolCall, ToolCall: &ToolCall{ID: "2", Name: "get_time", Arguments: []byte(`{}`)}},
		},
	}

	calls := ToolCalls(value)
	if len(calls) != 2 {
		t.Fatalf("ToolCalls() returned %d calls, want 2", len(calls))
	}
	if calls[0].ID != "1" || calls[0].Name != "get_weather" {
		t.Errorf("calls[0] = %+v, want ID=1 Name=get_weather", calls[0])
	}
	if calls[1].ID != "2" || calls[1].Name != "get_time" {
		t.Errorf("calls[1] = %+v, want ID=2 Name=get_time", calls[1])
	}
}

func TestToolCallsReturnsNilWithoutToolCallBlocks(t *testing.T) {
	value := Text(RoleAssistant, "hello")
	if calls := ToolCalls(value); calls != nil {
		t.Errorf("ToolCalls() = %v, want nil", calls)
	}
}

func TestToolCallsResultIsIndependentOfSource(t *testing.T) {
	value := Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{Kind: ContentToolCall, ToolCall: &ToolCall{ID: "1", Name: "f", Arguments: []byte(`{"a":1}`)}},
		},
	}

	calls := ToolCalls(value)
	calls[0].Arguments[2] = 'X'

	if string(value.Content[0].ToolCall.Arguments) != `{"a":1}` {
		t.Errorf("ToolCalls() result aliases source Arguments; source mutated to %q", value.Content[0].ToolCall.Arguments)
	}
}
