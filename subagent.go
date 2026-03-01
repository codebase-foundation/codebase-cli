package main

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Subagent — read-only research agent in isolated context
//
//  Modeled after the web app's dispatch_agent tool. Spawns a
//  separate agent loop with its own conversation history and
//  read-only tools. Returns only the final text summary.
//  Max depth 1 (subagents cannot spawn sub-subagents).
// ──────────────────────────────────────────────────────────────

const subagentMaxTurns = 15

const subagentSystemPrompt = `You are a focused research assistant. You help gather information by reading files, searching code, and listing directories.

You have read-only access to the project. You CANNOT modify any files.

Available tools: read_file, list_files, search_files, web_search, shell

Guidelines:
- Be thorough but efficient — gather what's needed, then summarize
- Use search_files to find relevant code quickly
- Use list_files to explore project structure
- Use web_search when you need external documentation, API references, or current information
- Use shell for read-only commands only (ls, cat, grep, git log, etc.)
- When done, provide a clear, concise summary of your findings`

// subagentToolDefs contains only read-only tools.
var subagentToolDefs []ToolDef

func init() {
	for _, td := range toolDefs {
		switch td.Function.Name {
		case "read_file", "list_files", "search_files", "web_search", "shell":
			subagentToolDefs = append(subagentToolDefs, td)
		}
	}
}

// RunSubagent executes a read-only research subagent and returns its final text.
func RunSubagent(client *LLMClient, workDir, task string) (string, error) {
	sysContent := subagentSystemPrompt + fmt.Sprintf("\n\nWorking directory: %s", workDir)

	history := []ChatMessage{
		{Role: "system", Content: strPtr(sysContent)},
		{Role: "user", Content: strPtr(task)},
	}

	var finalText strings.Builder

	for turn := 1; turn <= subagentMaxTurns; turn++ {
		// Check compaction
		if needsCompaction(history, client.Model) {
			compacted, ok := compactHistory(client, history)
			if ok {
				history = compacted
			}
		}

		// Stream LLM call
		streamCh := make(chan StreamEvent, 64)
		go client.StreamChat(history, subagentToolDefs, streamCh)

		var textContent strings.Builder
		var toolCalls []ToolCall

		for evt := range streamCh {
			switch evt.Type {
			case StreamText:
				textContent.WriteString(evt.Text)
			case StreamToolCalls:
				toolCalls = evt.ToolCalls
			case StreamError:
				return "", fmt.Errorf("subagent LLM error: %v", evt.Error)
			case StreamDone:
				// handled below
			}
		}

		// Build assistant message
		assistantMsg := ChatMessage{Role: "assistant"}
		txt := textContent.String()
		if txt != "" {
			assistantMsg.Content = strPtr(txt)
		}
		if len(toolCalls) > 0 {
			assistantMsg.ToolCalls = toolCalls
		}
		history = append(history, assistantMsg)

		// If no tool calls, we're done
		if len(toolCalls) == 0 {
			finalText.WriteString(txt)
			break
		}

		// Execute read-only tools (all parallel-safe)
		for _, tc := range toolCalls {
			// Safety: only allow read-only tools
			switch tc.Function.Name {
			case "read_file", "list_files", "search_files", "web_search", "shell":
				// shell is allowed but subagent should only use read-only commands
			default:
				history = append(history, ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    strPtr(fmt.Sprintf("Error: tool %q is not available in read-only mode", tc.Function.Name)),
				})
				continue
			}

			output, _ := ExecuteTool(tc.Function.Name, tc.Function.Arguments, workDir)
			history = append(history, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    strPtr(output),
			})
		}
	}

	result := finalText.String()
	if result == "" {
		result = "Subagent completed without producing a summary."
	}

	return result, nil
}
