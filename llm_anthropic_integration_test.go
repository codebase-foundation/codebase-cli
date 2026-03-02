package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// Integration tests — these call the real MiniMax Anthropic API.
// Run with: go test -v -run TestAnthropicIntegration -timeout 60s
// Requires OPENAI_API_KEY env var to be set.

func skipWithoutAPIKey(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set, skipping integration test")
	}
}

func TestAnthropicIntegrationBasicChat(t *testing.T) {
	skipWithoutAPIKey(t)

	client := newLLMClientWithTimeout(
		os.Getenv("OPENAI_API_KEY"),
		"https://api.minimax.io/anthropic",
		"MiniMax-M2.5",
		30*time.Second,
	)

	if client.Protocol != ProtocolAnthropic {
		t.Fatalf("expected anthropic protocol, got %s", client.Protocol)
	}

	messages := []ChatMessage{
		{Role: "system", Content: strPtr("You are a helpful assistant. Answer concisely.")},
		{Role: "user", Content: strPtr("What is 2 + 2? Reply with just the number.")},
	}

	ch := make(chan StreamEvent, 64)
	go client.StreamChat(context.Background(), messages, nil, ch)

	var text string
	var gotUsage bool
	var gotDone bool

	for evt := range ch {
		switch evt.Type {
		case StreamText:
			text += evt.Text
			fmt.Printf("[text] %s", evt.Text)
		case StreamUsage:
			gotUsage = true
			fmt.Printf("\n[usage] prompt=%d completion=%d\n", evt.Usage.PromptTokens, evt.Usage.CompletionTokens)
		case StreamDone:
			gotDone = true
			fmt.Println("[done]")
		case StreamError:
			t.Fatalf("stream error: %v", evt.Error)
		}
	}

	if text == "" {
		t.Error("expected text content, got empty")
	}
	if !gotUsage {
		t.Error("expected usage event")
	}
	if !gotDone {
		t.Error("expected done event")
	}
	t.Logf("Response: %q", text)
}

func TestAnthropicIntegrationToolUse(t *testing.T) {
	skipWithoutAPIKey(t)

	client := newLLMClientWithTimeout(
		os.Getenv("OPENAI_API_KEY"),
		"https://api.minimax.io/anthropic",
		"MiniMax-M2.5",
		30*time.Second,
	)

	messages := []ChatMessage{
		{Role: "system", Content: strPtr("You are a coding assistant. Use the read_file tool to read main.go.")},
		{Role: "user", Content: strPtr("Please read the file main.go")},
	}

	// Provide a simple tool
	tools := []ToolDef{
		{
			Type: "function",
			Function: ToolDefFunction{
				Name:        "read_file",
				Description: "Read a file and return its contents.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"path": map[string]interface{}{
							"type":        "string",
							"description": "Absolute or relative path to the file.",
						},
					},
					"required": []string{"path"},
				},
			},
		},
	}

	ch := make(chan StreamEvent, 64)
	go client.StreamChat(context.Background(), messages, tools, ch)

	var text string
	var toolCalls []ToolCall
	var gotDone bool

	for evt := range ch {
		switch evt.Type {
		case StreamText:
			text += evt.Text
			fmt.Printf("[text] %s", evt.Text)
		case StreamToolCalls:
			toolCalls = evt.ToolCalls
			fmt.Printf("\n[tool_calls] %d calls\n", len(toolCalls))
			for _, tc := range toolCalls {
				fmt.Printf("  tool: %s(%s) id=%s\n", tc.Function.Name, tc.Function.Arguments, tc.ID)
			}
		case StreamUsage:
			fmt.Printf("[usage] prompt=%d completion=%d\n", evt.Usage.PromptTokens, evt.Usage.CompletionTokens)
		case StreamDone:
			gotDone = true
			fmt.Println("[done]")
		case StreamError:
			t.Fatalf("stream error: %v", evt.Error)
		}
	}

	if !gotDone {
		t.Error("expected done event")
	}

	// The model should call read_file
	if len(toolCalls) == 0 {
		t.Error("expected at least one tool call")
		t.Logf("Text response instead: %q", text)
		return
	}

	tc := toolCalls[0]
	if tc.Function.Name != "read_file" {
		t.Errorf("expected read_file tool call, got %s", tc.Function.Name)
	}
	if tc.ID == "" {
		t.Error("tool call ID should not be empty")
	}
	t.Logf("Tool call: %s(%s)", tc.Function.Name, tc.Function.Arguments)
}

func TestAnthropicIntegrationMultiTurn(t *testing.T) {
	skipWithoutAPIKey(t)

	client := newLLMClientWithTimeout(
		os.Getenv("OPENAI_API_KEY"),
		"https://api.minimax.io/anthropic",
		"MiniMax-M2.5",
		30*time.Second,
	)

	// Simulate a multi-turn conversation with tool use
	messages := []ChatMessage{
		{Role: "system", Content: strPtr("You are a helpful coding assistant.")},
		{Role: "user", Content: strPtr("Read main.go for me")},
		{
			Role: "assistant",
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
			Content:    strPtr("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"),
		},
	}

	// No tools this turn — we want the model to summarize
	ch := make(chan StreamEvent, 64)
	go client.StreamChat(context.Background(), messages, nil, ch)

	var text string
	var gotDone bool

	for evt := range ch {
		switch evt.Type {
		case StreamText:
			text += evt.Text
		case StreamDone:
			gotDone = true
		case StreamError:
			t.Fatalf("stream error: %v", evt.Error)
		}
	}

	if !gotDone {
		t.Error("expected done event")
	}
	if text == "" {
		t.Error("expected text response about the file contents")
	}
	t.Logf("Multi-turn response: %q", text)
}

func TestAnthropicIntegrationNonStreaming(t *testing.T) {
	skipWithoutAPIKey(t)

	client := newLLMClientWithTimeout(
		os.Getenv("OPENAI_API_KEY"),
		"https://api.minimax.io/anthropic",
		"MiniMax-M2.5",
		30*time.Second,
	)

	messages := []ChatMessage{
		{Role: "system", Content: strPtr("Answer concisely in one word.")},
		{Role: "user", Content: strPtr("What color is the sky on a clear day?")},
	}

	result, err := nonStreamingChatAnthropic(client, messages)
	if err != nil {
		t.Fatalf("non-streaming error: %v", err)
	}

	if result == "" {
		t.Error("expected non-empty response")
	}
	t.Logf("Non-streaming response: %q", result)
}
