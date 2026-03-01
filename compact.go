package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Conversation Compaction
//
//  When the conversation history approaches the context window
//  limit, older messages are summarized via an LLM call while
//  recent messages are kept verbatim. This matches the web
//  app's ConversationManager.
// ──────────────────────────────────────────────────────────────

const (
	charsPerToken       = 3.8
	perMessageOverhead  = 4
	keepRecentMessages  = 8
	compactionThreshold = 0.75
)

// defaultContextWindows maps model families to their context window sizes.
var defaultContextWindows = map[string]int{
	"gpt-4o":         128000,
	"gpt-4o-mini":    128000,
	"gpt-4.1":        1000000,
	"gpt-4.1-mini":   1000000,
	"gpt-4.1-nano":   1000000,
	"gpt-5":          1000000,
	"o3":             200000,
	"o4-mini":        200000,
	"claude":         200000,
	"glm":            128000,
	"gemini":         1000000,
	"deepseek":       128000,
	"llama":          128000,
	"qwen":           128000,
}

// getContextWindow returns the estimated context window for a model.
func getContextWindow(model string) int {
	// Check exact match first
	if w, ok := defaultContextWindows[model]; ok {
		return w
	}
	// Check prefix match
	modelLower := strings.ToLower(model)
	for prefix, w := range defaultContextWindows {
		if strings.HasPrefix(modelLower, prefix) {
			return w
		}
	}
	// Default to 128k
	return 128000
}

// estimateMessageTokens estimates the token count for a single message.
func estimateMessageTokens(msg ChatMessage) int {
	chars := 0
	if msg.Content != nil {
		chars += len(*msg.Content)
	}
	if len(msg.ToolCalls) > 0 {
		data, _ := json.Marshal(msg.ToolCalls)
		chars += len(data)
	}
	return int(math.Ceil(float64(chars)/charsPerToken)) + perMessageOverhead
}

// estimateTotalTokens estimates the total token count for a message history.
func estimateTotalTokens(messages []ChatMessage) int {
	total := 0
	for _, msg := range messages {
		total += estimateMessageTokens(msg)
	}
	return total
}

// needsCompaction checks if the history needs compaction.
func needsCompaction(messages []ChatMessage, model string) bool {
	estimated := estimateTotalTokens(messages)
	threshold := float64(getContextWindow(model)) * compactionThreshold
	return float64(estimated) > threshold
}

const summarizationPrompt = `You are summarizing a coding session for handoff to the next turn of the agent.

Create a structured summary covering:

## Progress
- What has been accomplished so far (completed tasks, created/modified files)
- Key decisions made and why

## Context
- Important file paths and their purposes
- Dependencies, configurations, or constraints discovered
- User preferences or requirements established

## Current State
- What was just done (the most recent changes)
- Any errors encountered and how they were resolved

## Next Steps
- What remains to be done
- Any blockers or open questions

Be concise but complete. Include specific file paths, function names, and error messages — the next assistant needs enough detail to continue seamlessly without re-reading files.`

const summaryPrefix = "[Conversation compacted — summary of previous work follows]\n\n"

// compactHistory summarizes older messages and returns a compacted history.
// Returns the new history and true if compaction happened, or the original
// history and false if compaction was skipped or failed.
func compactHistory(client *LLMClient, messages []ChatMessage) ([]ChatMessage, bool) {
	// Find system message
	var systemMsg *ChatMessage
	var nonSystem []ChatMessage
	if len(messages) > 0 && messages[0].Role == "system" {
		systemMsg = &messages[0]
		nonSystem = messages[1:]
	} else {
		nonSystem = messages
	}

	// Not enough to compact
	if len(nonSystem) <= keepRecentMessages+2 {
		return messages, false
	}

	// Split: older to summarize, recent to keep
	splitAt := len(nonSystem) - keepRecentMessages

	// Don't split between a tool-call assistant message and its tool results.
	// Walk backward to find a safe split point.
	for splitAt > 0 && splitAt < len(nonSystem) {
		msg := nonSystem[splitAt]
		if msg.Role == "tool" {
			// This is a tool result — can't split here, move earlier
			splitAt--
			continue
		}
		// If the previous message is an assistant with tool_calls,
		// we'd orphan the tool results. Move back before it.
		if splitAt > 0 {
			prev := nonSystem[splitAt-1]
			if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
				splitAt--
				continue
			}
		}
		break
	}
	if splitAt <= 0 {
		return messages, false
	}

	toSummarize := nonSystem[:splitAt]
	toKeep := nonSystem[splitAt:]

	// Format history for summarization
	var sb strings.Builder
	for _, msg := range toSummarize {
		role := strings.ToUpper(msg.Role)
		if len(msg.ToolCalls) > 0 {
			fmt.Fprintf(&sb, "[%s] Tool calls:\n", role)
			for _, tc := range msg.ToolCalls {
				args := tc.Function.Arguments
				if len(args) > 200 {
					args = args[:200] + "..."
				}
				fmt.Fprintf(&sb, "  %s(%s)\n", tc.Function.Name, args)
			}
		} else if msg.Role == "tool" {
			content := ""
			if msg.Content != nil {
				content = *msg.Content
			}
			if len(content) > 500 {
				content = content[:500] + "..."
			}
			fmt.Fprintf(&sb, "[TOOL RESULT] %s:\n%s\n", msg.Name, content)
		} else {
			content := ""
			if msg.Content != nil {
				content = *msg.Content
			}
			if len(content) > 1000 {
				content = content[:1000] + "..."
			}
			fmt.Fprintf(&sb, "[%s] %s\n", role, content)
		}
		sb.WriteString("\n")
	}

	// Call LLM for summarization (non-streaming)
	summary, err := nonStreamingChat(client, []ChatMessage{
		{Role: "system", Content: strPtr(summarizationPrompt)},
		{Role: "user", Content: strPtr("Here is the conversation history to summarize:\n\n" + sb.String())},
	})
	if err != nil {
		// Don't crash — continue with full history
		return messages, false
	}

	// Rebuild: system + summary + ack + recent
	var compacted []ChatMessage
	if systemMsg != nil {
		compacted = append(compacted, *systemMsg)
	}
	compacted = append(compacted, ChatMessage{
		Role:    "user",
		Content: strPtr(summaryPrefix + summary),
	})
	compacted = append(compacted, ChatMessage{
		Role:    "assistant",
		Content: strPtr("I have the context from our previous conversation. Continuing from where we left off."),
	})
	compacted = append(compacted, toKeep...)

	return compacted, true
}

// nonStreamingChat makes a non-streaming Chat Completions call.
func nonStreamingChat(client *LLMClient, messages []ChatMessage) (string, error) {
	body := map[string]interface{}{
		"model":    client.Model,
		"messages": messages,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", client.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+client.APIKey)

	resp, err := client.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(errBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}

	return result.Choices[0].Message.Content, nil
}
