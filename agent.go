package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Agent event types (sent from agent goroutine → TUI)
// ──────────────────────────────────────────────────────────────

type EventType int

const (
	EventTextDelta  EventType = iota // streaming text chunk
	EventToolStart                   // tool execution starting
	EventToolResult                  // tool execution done
	EventUsage                       // token count update
	EventTurnStart                   // new agentic turn
	EventDone                        // agent finished all turns
	EventError                       // error occurred
)

type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

type AgentEvent struct {
	Type    EventType
	Text    string         // EventTextDelta
	Tool    string         // EventToolStart / EventToolResult — tool name
	Args    map[string]any // EventToolStart — parsed arguments
	Output  string         // EventToolResult — tool output
	Success bool           // EventToolResult
	Tokens  TokenUsage     // EventUsage
	Turn    int            // EventTurnStart
	Error   error          // EventError
}

// ──────────────────────────────────────────────────────────────
//  Agent
// ──────────────────────────────────────────────────────────────

const maxTurns = 25

const systemPrompt = `You are Codebase, a local AI coding agent running in the user's terminal.
You have direct access to their filesystem and shell. You help them build,
debug, and modify software projects.

Available tools: read_file, write_file, edit_file, shell

Guidelines:
- Read files before editing them — understand existing code first
- Make targeted, minimal changes — don't rewrite entire files unnecessarily
- Use shell for: installing packages, running tests, git operations, listing files
- All paths are relative to the working directory
- If a tool fails, read the error and try a different approach
- When finished, briefly summarize what you changed and why`

type Agent struct {
	client   *LLMClient
	workDir  string
	history  []ChatMessage
	events   chan<- AgentEvent
	stopCh   <-chan struct{}
	files    int // count of files created/modified
}

func NewAgent(client *LLMClient, workDir string, events chan<- AgentEvent, stopCh <-chan struct{}) *Agent {
	sysContent := systemPrompt + fmt.Sprintf("\n\nWorking directory: %s", workDir)
	return &Agent{
		client:  client,
		workDir: workDir,
		events:  events,
		stopCh:  stopCh,
		history: []ChatMessage{
			{Role: "system", Content: strPtr(sysContent)},
		},
	}
}

func strPtr(s string) *string { return &s }

// Run executes the agent loop for a user prompt. Blocks until done.
func (a *Agent) Run(prompt string) {
	a.history = append(a.history, ChatMessage{
		Role:    "user",
		Content: strPtr(prompt),
	})

	for turn := 1; turn <= maxTurns; turn++ {
		// Check for stop signal
		select {
		case <-a.stopCh:
			a.events <- AgentEvent{Type: EventDone, Text: "Stopped by user."}
			return
		default:
		}

		a.events <- AgentEvent{Type: EventTurnStart, Turn: turn}

		// Stream LLM call
		streamCh := make(chan StreamEvent, 64)
		go a.client.StreamChat(a.history, toolDefs, streamCh)

		var textContent strings.Builder
		var toolCalls []ToolCall
		var lastUsage ChunkUsage

		for evt := range streamCh {
			// Check stop between stream events
			select {
			case <-a.stopCh:
				a.events <- AgentEvent{Type: EventDone, Text: "Stopped by user."}
				return
			default:
			}

			switch evt.Type {
			case StreamText:
				textContent.WriteString(evt.Text)
				a.events <- AgentEvent{Type: EventTextDelta, Text: evt.Text}

			case StreamToolCalls:
				toolCalls = evt.ToolCalls

			case StreamUsage:
				lastUsage = evt.Usage
				a.events <- AgentEvent{
					Type:   EventUsage,
					Tokens: TokenUsage{PromptTokens: evt.Usage.PromptTokens, CompletionTokens: evt.Usage.CompletionTokens},
				}

			case StreamError:
				a.events <- AgentEvent{Type: EventError, Error: evt.Error}
				a.events <- AgentEvent{Type: EventDone, Text: "Error occurred."}
				return

			case StreamDone:
				// handled below
			}
		}

		_ = lastUsage

		// Build assistant message for history
		assistantMsg := ChatMessage{Role: "assistant"}
		txt := textContent.String()
		if txt != "" {
			assistantMsg.Content = strPtr(txt)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		a.history = append(a.history, assistantMsg)

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			a.events <- AgentEvent{Type: EventDone, Text: txt}
			return
		}

		// Execute each tool call
		for _, tc := range toolCalls {
			// Parse args for display
			var argsMap map[string]any
			json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)

			a.events <- AgentEvent{
				Type: EventToolStart,
				Tool: tc.Function.Name,
				Args: argsMap,
			}

			output, success := ExecuteTool(tc.Function.Name, tc.Function.Arguments, a.workDir)

			if success && (tc.Function.Name == "write_file" || tc.Function.Name == "edit_file") {
				a.files++
			}

			a.events <- AgentEvent{
				Type:    EventToolResult,
				Tool:    tc.Function.Name,
				Args:    argsMap,
				Output:  output,
				Success: success,
			}

			// Add tool result to history
			a.history = append(a.history, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    strPtr(output),
			})
		}

		// Loop back for next turn
	}

	a.events <- AgentEvent{Type: EventDone, Text: "Reached maximum turns."}
}

// FilesChanged returns how many files the agent has created/modified.
func (a *Agent) FilesChanged() int {
	return a.files
}
