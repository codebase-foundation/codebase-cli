package main

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func testConfig() *Config {
	return &Config{
		APIKey:  "test-key",
		BaseURL: "http://localhost:9999",
		Model:   "test-model",
		WorkDir: "/tmp",
	}
}

func initChat(t *testing.T) chatModel {
	t.Helper()
	m := newChatModel(testConfig())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if !m.ready {
		t.Fatal("viewport not initialized after WindowSizeMsg")
	}
	return m
}

// simulateRound feeds a full agent conversation through the Update loop:
// TurnStart → TextDeltas → Done → flashTicks until idle.
func simulateRound(t *testing.T, m chatModel, prompt string, responseChunks []string) chatModel {
	t.Helper()

	// Manually set up streaming state (avoids real HTTP goroutine)
	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()
	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n  > " + prompt + "\n\n",
	})
	m.input.SetValue("")

	events := []AgentEvent{
		{Type: EventTurnStart, Turn: 1},
	}
	for _, chunk := range responseChunks {
		events = append(events, AgentEvent{Type: EventTextDelta, Text: chunk})
	}
	events = append(events, AgentEvent{
		Type:   EventUsage,
		Tokens: TokenUsage{PromptTokens: 100, CompletionTokens: 50},
	})
	events = append(events, AgentEvent{Type: EventDone, Text: "done"})

	for _, evt := range events {
		m, _ = m.Update(agentEventMsg(evt))
	}

	if m.state != chatDoneFlash {
		t.Fatalf("expected chatDoneFlash after EventDone, got %d", m.state)
	}

	// Drain flash ticks until idle
	for i := 0; i < 10; i++ {
		if m.state == chatIdle {
			break
		}
		m, _ = m.Update(flashTickMsg{})
	}

	if m.state != chatIdle {
		t.Fatalf("state stuck at %d after flash ticks (flashFrames=%d)", m.state, m.flashFrames)
	}

	return m
}

// TestMultiRoundConversation simulates 5 consecutive prompts and verifies
// the model returns to chatIdle after each one.
func TestMultiRoundConversation(t *testing.T) {
	m := initChat(t)

	for round := 1; round <= 5; round++ {
		chunks := []string{"Hello ", "from ", "round ", fmt.Sprintf("%d", round), "!"}
		m = simulateRound(t, m, "test prompt", chunks)

		if m.state != chatIdle {
			t.Fatalf("round %d: state=%d, expected chatIdle", round, m.state)
		}
		if m.streaming.Len() != 0 {
			t.Fatalf("round %d: streaming buffer not empty (%d bytes)", round, m.streaming.Len())
		}
		v := m.View()
		if v == "" {
			t.Fatalf("round %d: empty view", round)
		}
	}
}

// TestToolCallRound simulates a round with tool calls.
func TestToolCallRound(t *testing.T) {
	m := initChat(t)

	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()
	m.segments = append(m.segments, segment{
		kind: "user",
		text: "\n  > build something\n\n",
	})

	events := []AgentEvent{
		{Type: EventTurnStart, Turn: 1},
		{Type: EventTextDelta, Text: "Let me read the file first."},
		{Type: EventToolStart, Tool: "read_file", Args: map[string]any{"path": "main.go"}},
		{Type: EventToolResult, Tool: "read_file", Output: "package main\n", Success: true, Args: map[string]any{"path": "main.go"}},
		{Type: EventTurnStart, Turn: 2},
		{Type: EventTextDelta, Text: "Now writing the file."},
		{Type: EventToolStart, Tool: "write_file", Args: map[string]any{"path": "out.go", "content": "package main"}},
		{Type: EventToolResult, Tool: "write_file", Output: "OK", Success: true, Args: map[string]any{"path": "out.go"}},
		{Type: EventTextDelta, Text: " Done!"},
		{Type: EventDone, Text: "done"},
	}

	for _, evt := range events {
		m, _ = m.Update(agentEventMsg(evt))
	}

	for i := 0; i < 10 && m.state != chatIdle; i++ {
		m, _ = m.Update(flashTickMsg{})
	}

	if m.state != chatIdle {
		t.Fatalf("state stuck at %d after tool call round", m.state)
	}

	toolCount := 0
	for _, seg := range m.segments {
		if seg.kind == "tool" {
			toolCount++
			if seg.tool.state == "pending" {
				t.Error("tool segment still pending after round complete")
			}
		}
	}
	if toolCount != 2 {
		t.Errorf("expected 2 tool segments, got %d", toolCount)
	}
}

// TestErrorRecovery verifies the model recovers from API errors.
func TestErrorRecovery(t *testing.T) {
	m := initChat(t)

	// Round 1: error
	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()

	events := []AgentEvent{
		{Type: EventTurnStart, Turn: 1},
		{Type: EventError, Error: fmt.Errorf("API 429: rate limited")},
		{Type: EventDone, Text: "error"},
	}

	for _, evt := range events {
		m, _ = m.Update(agentEventMsg(evt))
	}

	for i := 0; i < 10 && m.state != chatIdle; i++ {
		m, _ = m.Update(flashTickMsg{})
	}

	if m.state != chatIdle {
		t.Fatalf("state stuck at %d after error round", m.state)
	}

	// Round 2: should recover
	m = simulateRound(t, m, "try again", []string{"Success!"})
	if m.state != chatIdle {
		t.Fatal("state not idle after recovery round")
	}
}

// TestCopySafety ensures strings.Builder pointer survives value copies.
func TestCopySafety(t *testing.T) {
	m := initChat(t)

	m.streaming.WriteString("some text")

	// Copy by value (as Bubble Tea does)
	m2 := m

	m2.streaming.WriteString(" more text")

	if m.streaming.String() != "some text more text" {
		t.Error("pointer builder not shared across copies")
	}
}

// TestFlashTickDrain verifies flash ticks properly transition to idle.
func TestFlashTickDrain(t *testing.T) {
	m := initChat(t)
	m.state = chatDoneFlash
	m.flashFrames = 3

	states := []chatState{}
	for i := 0; i < 5; i++ {
		m, _ = m.Update(flashTickMsg{})
		states = append(states, m.state)
	}

	expected := []chatState{chatDoneFlash, chatDoneFlash, chatIdle, chatIdle, chatIdle}
	for i, s := range states {
		if s != expected[i] {
			t.Errorf("tick %d: state=%d, expected=%d", i, s, expected[i])
		}
	}
}

// TestHTTPTimeout verifies the LLM client has a timeout set.
func TestHTTPTimeout(t *testing.T) {
	client := NewLLMClient("key", "http://localhost", "model")
	if client.client.Timeout == 0 {
		t.Error("HTTP client has no timeout — will hang forever on slow APIs")
	}
	if client.client.Timeout < 30*time.Second {
		t.Errorf("HTTP timeout too short: %v", client.client.Timeout)
	}
}
