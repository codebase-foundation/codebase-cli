package main

import (
	"encoding/json"
	"os"
	"testing"
)

// ──────────────────────────────────────────────────────────────
//  Protocol detection tests
// ──────────────────────────────────────────────────────────────

func TestDetectProtocolOpenAI(t *testing.T) {
	p := detectProtocol("https://api.openai.com/v1")
	if p != ProtocolOpenAI {
		t.Errorf("expected openai, got %s", p)
	}
}

func TestDetectProtocolMiniMaxV1(t *testing.T) {
	p := detectProtocol("https://api.minimax.io/v1")
	if p != ProtocolOpenAI {
		t.Errorf("expected openai for minimax v1, got %s", p)
	}
}

func TestDetectProtocolMiniMaxAnthropic(t *testing.T) {
	p := detectProtocol("https://api.minimax.io/anthropic")
	if p != ProtocolAnthropic {
		t.Errorf("expected anthropic, got %s", p)
	}
}

func TestDetectProtocolAnthropicDotCom(t *testing.T) {
	p := detectProtocol("https://api.anthropic.com")
	if p != ProtocolAnthropic {
		t.Errorf("expected anthropic, got %s", p)
	}
}

func TestDetectProtocolEnvOverride(t *testing.T) {
	os.Setenv("LLM_PROTOCOL", "anthropic")
	defer os.Unsetenv("LLM_PROTOCOL")

	p := detectProtocol("https://api.openai.com/v1")
	if p != ProtocolAnthropic {
		t.Errorf("env override should force anthropic, got %s", p)
	}
}

func TestDetectProtocolEnvClaude(t *testing.T) {
	os.Setenv("LLM_PROTOCOL", "claude")
	defer os.Unsetenv("LLM_PROTOCOL")

	p := detectProtocol("https://api.minimax.io/v1")
	if p != ProtocolAnthropic {
		t.Errorf("env LLM_PROTOCOL=claude should map to anthropic, got %s", p)
	}
}

// ──────────────────────────────────────────────────────────────
//  Message conversion tests
// ──────────────────────────────────────────────────────────────

func TestConvertMessagesBasic(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("You are helpful.")},
		{Role: "user", Content: strPtr("Hello")},
		{Role: "assistant", Content: strPtr("Hi there!")},
	}

	system, anthMsgs := convertMessagesToAnthropic(msgs)

	if system != "You are helpful." {
		t.Errorf("system = %q, want %q", system, "You are helpful.")
	}
	if len(anthMsgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(anthMsgs))
	}
	if anthMsgs[0].Role != "user" {
		t.Errorf("msg[0].Role = %s, want user", anthMsgs[0].Role)
	}
	if anthMsgs[1].Role != "assistant" {
		t.Errorf("msg[1].Role = %s, want assistant", anthMsgs[1].Role)
	}
}

func TestConvertMessagesMultipleSystemPrompts(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("You are helpful.")},
		{Role: "user", Content: strPtr("Hello")},
		{Role: "assistant", Content: strPtr("Hi!")},
		{Role: "system", Content: strPtr("Also be concise.")},
		{Role: "user", Content: strPtr("Continue")},
	}

	system, _ := convertMessagesToAnthropic(msgs)

	if system != "You are helpful.\n\nAlso be concise." {
		t.Errorf("system = %q, want merged prompts", system)
	}
}

func TestConvertMessagesWithToolCalls(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("system")},
		{Role: "user", Content: strPtr("Read main.go")},
		{
			Role:    "assistant",
			Content: strPtr("Let me read that file."),
			ToolCalls: []ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"main.go"}`,
					},
				},
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_1",
			Name:       "read_file",
			Content:    strPtr("package main\n"),
		},
	}

	_, anthMsgs := convertMessagesToAnthropic(msgs)

	// Should be: user, assistant(text+tool_use), user(tool_result)
	if len(anthMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(anthMsgs))
	}

	// Check assistant message has content blocks
	assistantContent, ok := anthMsgs[1].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("assistant content should be []anthropicContentBlock, got %T", anthMsgs[1].Content)
	}
	if len(assistantContent) != 2 {
		t.Fatalf("expected 2 blocks (text + tool_use), got %d", len(assistantContent))
	}
	if assistantContent[0].Type != "text" {
		t.Errorf("block[0].Type = %s, want text", assistantContent[0].Type)
	}
	if assistantContent[1].Type != "tool_use" {
		t.Errorf("block[1].Type = %s, want tool_use", assistantContent[1].Type)
	}
	if assistantContent[1].ID != "call_1" {
		t.Errorf("tool_use ID = %s, want call_1", assistantContent[1].ID)
	}
	if assistantContent[1].Name != "read_file" {
		t.Errorf("tool_use Name = %s, want read_file", assistantContent[1].Name)
	}

	// Check tool result is in a user message
	if anthMsgs[2].Role != "user" {
		t.Errorf("tool result should be in user message, got %s", anthMsgs[2].Role)
	}
	toolContent, ok := anthMsgs[2].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("tool result content should be []anthropicContentBlock, got %T", anthMsgs[2].Content)
	}
	if len(toolContent) != 1 {
		t.Fatalf("expected 1 tool_result block, got %d", len(toolContent))
	}
	if toolContent[0].Type != "tool_result" {
		t.Errorf("block type = %s, want tool_result", toolContent[0].Type)
	}
	if toolContent[0].ToolUseID != "call_1" {
		t.Errorf("tool_use_id = %s, want call_1", toolContent[0].ToolUseID)
	}
}

func TestConvertMessagesConsecutiveToolResults(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("system")},
		{Role: "user", Content: strPtr("Do stuff")},
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
				{ID: "call_2", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"b.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call_1", Name: "read_file", Content: strPtr("file a")},
		{Role: "tool", ToolCallID: "call_2", Name: "read_file", Content: strPtr("file b")},
	}

	_, anthMsgs := convertMessagesToAnthropic(msgs)

	// Should be: user, assistant(2 tool_use), user(2 tool_result)
	if len(anthMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(anthMsgs))
	}

	// Both tool results should be in one user message
	toolContent, ok := anthMsgs[2].Content.([]anthropicContentBlock)
	if !ok {
		t.Fatalf("expected []anthropicContentBlock for tool results, got %T", anthMsgs[2].Content)
	}
	if len(toolContent) != 2 {
		t.Errorf("expected 2 tool_result blocks merged, got %d", len(toolContent))
	}
}

func TestConvertMessagesFirstMustBeUser(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "assistant", Content: strPtr("I'll help")},
		{Role: "user", Content: strPtr("Hello")},
	}

	_, anthMsgs := convertMessagesToAnthropic(msgs)

	if len(anthMsgs) == 0 {
		t.Fatal("expected messages")
	}
	if anthMsgs[0].Role != "user" {
		t.Errorf("first message should be user, got %s", anthMsgs[0].Role)
	}
}

// ──────────────────────────────────────────────────────────────
//  Tool definition conversion tests
// ──────────────────────────────────────────────────────────────

func TestConvertToolDefs(t *testing.T) {
	tools := []ToolDef{
		{
			Type: "function",
			Function: ToolDefFunction{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type":        "string",
							"description": "File path",
						},
					},
					"required": []string{"path"},
				},
			},
		},
	}

	result := convertToolDefsToAnthropic(tools)

	if len(result) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(result))
	}
	if result[0].Name != "read_file" {
		t.Errorf("name = %s, want read_file", result[0].Name)
	}
	if result[0].Description != "Read a file" {
		t.Errorf("description = %s, want 'Read a file'", result[0].Description)
	}
	// InputSchema should be the same as Parameters
	schemaJSON, _ := json.Marshal(result[0].InputSchema)
	if len(schemaJSON) < 10 {
		t.Errorf("input_schema seems too short: %s", schemaJSON)
	}
}

// ──────────────────────────────────────────────────────────────
//  Anthropic request serialization
// ──────────────────────────────────────────────────────────────

func TestAnthropicRequestSerialization(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("Be helpful.")},
		{Role: "user", Content: strPtr("Hello")},
	}

	system, anthMsgs := convertMessagesToAnthropic(msgs)

	req := anthropicRequest{
		Model:     "MiniMax-M2.5",
		System:    system,
		Messages:  anthMsgs,
		MaxTokens: 16384,
		Stream:    true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Verify it's valid JSON with expected fields
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if parsed["model"] != "MiniMax-M2.5" {
		t.Errorf("model = %v, want MiniMax-M2.5", parsed["model"])
	}
	if parsed["system"] != "Be helpful." {
		t.Errorf("system = %v, want 'Be helpful.'", parsed["system"])
	}
	if parsed["max_tokens"].(float64) != 16384 {
		t.Errorf("max_tokens = %v, want 16384", parsed["max_tokens"])
	}
	if parsed["stream"] != true {
		t.Errorf("stream = %v, want true", parsed["stream"])
	}
}

// ──────────────────────────────────────────────────────────────
//  LLMClient protocol field
// ──────────────────────────────────────────────────────────────

func TestLLMClientProtocolOpenAI(t *testing.T) {
	client := NewLLMClient("key", "https://api.minimax.io/v1", "model")
	if client.Protocol != ProtocolOpenAI {
		t.Errorf("expected openai protocol, got %s", client.Protocol)
	}
}

func TestLLMClientProtocolAnthropic(t *testing.T) {
	client := NewLLMClient("key", "https://api.minimax.io/anthropic", "model")
	if client.Protocol != ProtocolAnthropic {
		t.Errorf("expected anthropic protocol, got %s", client.Protocol)
	}
}

// ──────────────────────────────────────────────────────────────
//  Alternating message enforcement
// ──────────────────────────────────────────────────────────────

func TestEnsureAlternatingBasic(t *testing.T) {
	msgs := []anthropicMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi"},
		{Role: "user", Content: "Bye"},
	}
	result := ensureAlternating(msgs)
	if len(result) != 3 {
		t.Errorf("expected 3 messages, got %d", len(result))
	}
}

func TestEnsureAlternatingConsecutiveUser(t *testing.T) {
	msgs := []anthropicMessage{
		{Role: "user", Content: "Hello"},
		{Role: "user", Content: "Another"},
	}
	result := ensureAlternating(msgs)
	// Should insert an assistant bridge between them
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[1].Role != "assistant" {
		t.Errorf("bridge should be assistant, got %s", result[1].Role)
	}
}

func TestEnsureAlternatingStartsWithAssistant(t *testing.T) {
	msgs := []anthropicMessage{
		{Role: "assistant", Content: "Hi"},
	}
	result := ensureAlternating(msgs)
	if result[0].Role != "user" {
		t.Errorf("first message should be user, got %s", result[0].Role)
	}
}

// ──────────────────────────────────────────────────────────────
//  Multi-turn conversation with tools
// ──────────────────────────────────────────────────────────────

func TestConvertMessagesMultiTurnTools(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: strPtr("system")},
		{Role: "user", Content: strPtr("Fix the bug")},
		// Turn 1: read file
		{
			Role: "assistant",
			ToolCalls: []ToolCall{
				{ID: "c1", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"main.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "c1", Name: "read_file", Content: strPtr("package main")},
		// Turn 2: edit file
		{
			Role:    "assistant",
			Content: strPtr("I see the issue."),
			ToolCalls: []ToolCall{
				{ID: "c2", Type: "function", Function: FunctionCall{Name: "edit_file", Arguments: `{"path":"main.go","old_text":"old","new_text":"new"}`}},
			},
		},
		{Role: "tool", ToolCallID: "c2", Name: "edit_file", Content: strPtr("OK")},
		// Turn 3: final answer
		{Role: "assistant", Content: strPtr("Fixed!")},
	}

	system, anthMsgs := convertMessagesToAnthropic(msgs)

	if system != "system" {
		t.Errorf("system = %q", system)
	}

	// Expected: user, assistant(tool_use), user(tool_result), assistant(text+tool_use), user(tool_result), assistant(text)
	if len(anthMsgs) != 6 {
		t.Fatalf("expected 6 messages, got %d", len(anthMsgs))
	}

	// Verify alternation
	expectedRoles := []string{"user", "assistant", "user", "assistant", "user", "assistant"}
	for i, expected := range expectedRoles {
		if anthMsgs[i].Role != expected {
			t.Errorf("msg[%d].Role = %s, want %s", i, anthMsgs[i].Role, expected)
		}
	}
}
