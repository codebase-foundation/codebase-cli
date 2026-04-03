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
	"minimax":        204800,
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

// Structured summarization prompt — 9 sections matching CC's best practices,
// plus analysis/summary split where the analysis acts as a scratchpad that
// gets stripped before injection. This produces better summaries because
// the model "thinks" in <analysis> before committing to <summary>.
const summarizationPrompt = `CRITICAL: Respond with TEXT ONLY. Do NOT call any tools. You already have all the context you need.

Your task is to create a detailed summary of the coding session so far. The assistant who reads this summary will continue the work — they need enough detail to proceed without re-reading files.

First, write an <analysis> section where you think through what matters. Then write a <summary> section with the actual handoff content. The analysis is a scratchpad — only the summary will be kept.

Your summary MUST include these sections:

1. **Primary Request and Intent** — What the user asked for, including nuances and constraints
2. **Key Technical Decisions** — Architecture choices, libraries picked, approaches chosen and why
3. **Files Modified** — Every file created, modified, or deleted with what changed. Include paths.
4. **Code Context** — Important functions, types, variables, and patterns. Include actual signatures and snippets for anything the next assistant will need to reference
5. **Errors and Fixes** — Every error encountered, what caused it, and how it was resolved. Include the exact error text.
6. **User Messages** — ALL user messages that aren't tool results. Their exact words matter for intent.
7. **Current State** — What was just done in the most recent turn. What files are open/hot.
8. **Pending Work** — What still needs to be done, in order of priority
9. **Open Questions** — Anything unresolved, uncertain, or that needs user input

Be specific: include file paths, line numbers, function names, error messages, and code snippets. Vague summaries are useless.`

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

	// Call LLM for summarization — use glue (cheap/fast) model if available,
	// fall back to main model. This is a differentiator over CC which always
	// uses the main model for compaction.
	summary, err := nonStreamingChat(client, []ChatMessage{
		{Role: "system", Content: strPtr(summarizationPrompt)},
		{Role: "user", Content: strPtr("Here is the conversation history to summarize:\n\n" + sb.String())},
	})
	if err != nil {
		// Don't crash — continue with full history
		return messages, false
	}

	// Strip <analysis> section — it's a thinking scratchpad, not informational.
	// Keep only <summary> content (or everything if no tags found).
	summary = stripAnalysis(summary)

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
		Content: strPtr("I have the full context from our previous work. Continuing seamlessly."),
	})
	compacted = append(compacted, toKeep...)

	return compacted, true
}

// stripAnalysis removes <analysis>...</analysis> blocks and extracts
// <summary>...</summary> content. If no tags found, returns the original.
func stripAnalysis(text string) string {
	// Remove analysis block
	if idx := strings.Index(text, "<analysis>"); idx >= 0 {
		if end := strings.Index(text, "</analysis>"); end >= 0 {
			text = text[:idx] + text[end+len("</analysis>"):]
		}
	}

	// Extract summary content if tagged
	if idx := strings.Index(text, "<summary>"); idx >= 0 {
		if end := strings.Index(text, "</summary>"); end >= 0 {
			text = strings.TrimSpace(text[idx+len("<summary>") : end])
		}
	}

	return strings.TrimSpace(text)
}

// nonStreamingChat makes a non-streaming LLM call, dispatching by protocol.
func nonStreamingChat(client *LLMClient, messages []ChatMessage) (string, error) {
	if client.Protocol == ProtocolMessages {
		return nonStreamingChatAnthropic(client, messages)
	}
	return nonStreamingChatOpenAI(client, messages)
}

// nonStreamingChatOpenAI makes a non-streaming OpenAI Chat Completions call.
func nonStreamingChatOpenAI(client *LLMClient, messages []ChatMessage) (string, error) {
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
