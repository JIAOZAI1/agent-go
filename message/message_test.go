package message

import "testing"

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
