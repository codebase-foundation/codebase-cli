package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Anthropic Messages API — request types
// ──────────────────────────────────────────────────────────────

type anthropicRequest struct {
	Model     string             `json:"model"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens"`
	Stream    bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []anthropicContentBlock
}

type anthropicContentBlock struct {
	Type string `json:"type"`

	// text block
	Text string `json:"text,omitempty"`

	// tool_use block
	ID    string      `json:"id,omitempty"`
	Name  string      `json:"name,omitempty"`
	Input interface{} `json:"input,omitempty"`

	// tool_result block
	ToolUseID string      `json:"tool_use_id,omitempty"`
	Content   interface{} `json:"content,omitempty"` // string or []contentBlock for tool_result
	IsError   bool        `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"input_schema"`
}

// ──────────────────────────────────────────────────────────────
//  Anthropic Messages API — SSE response types
// ──────────────────────────────────────────────────────────────

type anthropicSSE struct {
	Type string `json:"type"`

	// message_start
	Message *anthropicResponseMessage `json:"message,omitempty"`

	// content_block_start
	Index        *int                   `json:"index,omitempty"`
	ContentBlock *anthropicContentBlock `json:"content_block,omitempty"`

	// content_block_delta
	Delta *anthropicDelta `json:"delta,omitempty"`

	// message_delta
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type anthropicResponseMessage struct {
	ID    string         `json:"id"`
	Model string         `json:"model"`
	Usage *anthropicUsage `json:"usage,omitempty"`
}

type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// ──────────────────────────────────────────────────────────────
//  Message conversion: ChatMessage → Anthropic format
// ──────────────────────────────────────────────────────────────

// convertMessagesToAnthropic transforms our internal ChatMessage slice
// into the Anthropic Messages API format. Returns the system prompt
// (extracted from system role messages) and the converted message list.
func convertMessagesToAnthropic(messages []ChatMessage) (string, []anthropicMessage) {
	var systemParts []string
	var result []anthropicMessage

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		switch msg.Role {
		case "system":
			// All system messages become part of the top-level system field
			if msg.Content != nil && *msg.Content != "" {
				systemParts = append(systemParts, *msg.Content)
			}

		case "user":
			content := ""
			if msg.Content != nil {
				content = *msg.Content
			}
			// Try to merge with the previous message if it's also a user message
			if len(result) > 0 && result[len(result)-1].Role == "user" {
				prev := &result[len(result)-1]
				prev.Content = mergeUserContent(prev.Content, content)
			} else {
				result = append(result, anthropicMessage{
					Role:    "user",
					Content: content,
				})
			}

		case "assistant":
			var blocks []anthropicContentBlock

			// Add text content
			if msg.Content != nil && *msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: *msg.Content,
				})
			}

			// Add tool_use blocks from tool calls
			for _, tc := range msg.ToolCalls {
				var inputObj interface{}
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &inputObj); err != nil {
					// If we can't parse as JSON, wrap as raw string
					inputObj = map[string]interface{}{"_raw": tc.Function.Arguments}
				}
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: inputObj,
				})
			}

			if len(blocks) == 0 {
				// Empty assistant message — add minimal text
				blocks = append(blocks, anthropicContentBlock{
					Type: "text",
					Text: "",
				})
			}

			result = append(result, anthropicMessage{
				Role:    "assistant",
				Content: blocks,
			})

		case "tool":
			// Tool results become part of a user message with tool_result blocks.
			// Collect consecutive tool messages.
			var toolBlocks []anthropicContentBlock
			for i < len(messages) && messages[i].Role == "tool" {
				tmsg := messages[i]
				content := ""
				if tmsg.Content != nil {
					content = *tmsg.Content
				}
				toolBlocks = append(toolBlocks, anthropicContentBlock{
					Type:      "tool_result",
					ToolUseID: tmsg.ToolCallID,
					Content:   content,
				})
				i++
			}
			i-- // outer loop will increment

			// If the last result message was a user, merge into it
			if len(result) > 0 && result[len(result)-1].Role == "user" {
				prev := &result[len(result)-1]
				prev.Content = mergeToolResults(prev.Content, toolBlocks)
			} else {
				result = append(result, anthropicMessage{
					Role:    "user",
					Content: toolBlocks,
				})
			}
		}
	}

	// Anthropic requires alternating user/assistant. Ensure we don't have
	// consecutive messages of the same role (except user→user is handled above).
	result = ensureAlternating(result)

	system := strings.Join(systemParts, "\n\n")
	return system, result
}

// mergeUserContent merges a text string into an existing user content.
func mergeUserContent(existing interface{}, newText string) interface{} {
	switch v := existing.(type) {
	case string:
		if newText == "" {
			return v
		}
		return v + "\n\n" + newText
	case []anthropicContentBlock:
		if newText != "" {
			v = append(v, anthropicContentBlock{Type: "text", Text: newText})
		}
		return v
	default:
		return newText
	}
}

// mergeToolResults merges tool_result blocks into existing user content.
func mergeToolResults(existing interface{}, results []anthropicContentBlock) interface{} {
	switch v := existing.(type) {
	case string:
		// Convert text to blocks, then append tool results
		blocks := []anthropicContentBlock{{Type: "text", Text: v}}
		blocks = append(blocks, results...)
		return blocks
	case []anthropicContentBlock:
		return append(v, results...)
	default:
		return results
	}
}

// ensureAlternating fixes consecutive same-role messages by inserting
// minimal bridging messages where needed.
func ensureAlternating(msgs []anthropicMessage) []anthropicMessage {
	if len(msgs) == 0 {
		return msgs
	}

	var fixed []anthropicMessage
	fixed = append(fixed, msgs[0])

	for i := 1; i < len(msgs); i++ {
		prev := fixed[len(fixed)-1]
		curr := msgs[i]

		if prev.Role == curr.Role {
			// Insert a bridge message of the opposite role
			if curr.Role == "user" {
				fixed = append(fixed, anthropicMessage{
					Role:    "assistant",
					Content: "Understood.",
				})
			} else {
				fixed = append(fixed, anthropicMessage{
					Role:    "user",
					Content: "Continue.",
				})
			}
		}
		fixed = append(fixed, curr)
	}

	// Anthropic requires the first message to be user role
	if len(fixed) > 0 && fixed[0].Role != "user" {
		fixed = append([]anthropicMessage{{
			Role:    "user",
			Content: "Begin.",
		}}, fixed...)
	}

	return fixed
}

// ──────────────────────────────────────────────────────────────
//  Tool definition conversion
// ──────────────────────────────────────────────────────────────

func convertToolDefsToAnthropic(tools []ToolDef) []anthropicTool {
	result := make([]anthropicTool, len(tools))
	for i, td := range tools {
		result[i] = anthropicTool{
			Name:        td.Function.Name,
			Description: td.Function.Description,
			InputSchema: td.Function.Parameters,
		}
	}
	return result
}

// ──────────────────────────────────────────────────────────────
//  Streaming: Anthropic Messages API
// ──────────────────────────────────────────────────────────────

func (c *LLMClient) streamChatAnthropic(ctx context.Context, messages []ChatMessage, tools []ToolDef, ch chan<- StreamEvent) {
	defer close(ch)

	system, anthMsgs := convertMessagesToAnthropic(messages)

	req := anthropicRequest{
		Model:     c.Model,
		System:    system,
		Messages:  anthMsgs,
		MaxTokens: 16384,
		Stream:    true,
	}

	if len(tools) > 0 {
		req.Tools = convertToolDefsToAnthropic(tools)
	}

	jsonBody, err := json.Marshal(req)
	if err != nil {
		ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("marshal: %w", err)}
		return
	}

	// Retry transient errors
	var resp *http.Response
	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(jsonBody))
		if err != nil {
			ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("request: %w", err)}
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("x-api-key", c.APIKey)
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err = c.client.Do(httpReq)
		if err != nil {
			ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("connection error: %v", err)}
			return
		}

		if resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 {
			resp.Body.Close()
			if attempt < maxRetries {
				backoff := time.Duration(1<<uint(attempt)) * time.Second
				time.Sleep(backoff)
				continue
			}
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("API error %d: %s", resp.StatusCode, truncateErrorBody(string(errBody)))}
		return
	}

	// Parse Anthropic SSE stream
	type toolAccum struct {
		id   string
		name string
		json strings.Builder
	}

	var (
		inputTokens  int
		outputTokens int
		tools_       []toolAccum
		currentIdx   int = -1
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// Skip event: lines and empty lines
		if strings.HasPrefix(line, "event:") || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var evt anthropicSSE
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue // skip malformed
		}

		switch evt.Type {
		case "message_start":
			if evt.Message != nil && evt.Message.Usage != nil {
				inputTokens = evt.Message.Usage.InputTokens
			}

		case "content_block_start":
			if evt.ContentBlock != nil {
				idx := 0
				if evt.Index != nil {
					idx = *evt.Index
				}
				currentIdx = idx

				if evt.ContentBlock.Type == "tool_use" {
					// Grow tool accumulator slice
					for len(tools_) <= idx {
						tools_ = append(tools_, toolAccum{})
					}
					tools_[idx].id = evt.ContentBlock.ID
					tools_[idx].name = evt.ContentBlock.Name
				}
			}

		case "content_block_delta":
			if evt.Delta == nil {
				continue
			}

			switch evt.Delta.Type {
			case "text_delta":
				if evt.Delta.Text != "" {
					ch <- StreamEvent{Type: StreamText, Text: evt.Delta.Text}
				}

			case "input_json_delta":
				// Accumulate tool call JSON
				idx := currentIdx
				if evt.Index != nil {
					idx = *evt.Index
				}
				if idx >= 0 && idx < len(tools_) {
					tools_[idx].json.WriteString(evt.Delta.PartialJSON)
				}

			case "thinking_delta":
				// Skip thinking/reasoning tokens
			}

		case "content_block_stop":
			// Block finalized — nothing special needed

		case "message_delta":
			if evt.Usage != nil {
				outputTokens = evt.Usage.OutputTokens
			}
			if evt.Delta != nil && evt.Delta.StopReason != "" {
				// Emit usage
				ch <- StreamEvent{
					Type: StreamUsage,
					Usage: ChunkUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
					},
				}

				// If there are tool calls, emit them
				if len(tools_) > 0 {
					var toolCalls []ToolCall
					for _, ta := range tools_ {
						if ta.id == "" {
							continue
						}
						toolCalls = append(toolCalls, ToolCall{
							ID:   ta.id,
							Type: "function",
							Function: FunctionCall{
								Name:      ta.name,
								Arguments: ta.json.String(),
							},
						})
					}
					if len(toolCalls) > 0 {
						ch <- StreamEvent{Type: StreamToolCalls, ToolCalls: toolCalls}
					}
				}
			}

		case "message_stop":
			ch <- StreamEvent{Type: StreamDone}
			return

		case "error":
			// Anthropic can send error events in the stream
			errMsg := data
			if evt.Delta != nil {
				errMsg = fmt.Sprintf("stream error: %s", data)
			}
			ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("Anthropic stream error: %s", errMsg)}
			return
		}
	}

	// If we get here without message_stop, still emit done
	ch <- StreamEvent{Type: StreamDone}
}

// ──────────────────────────────────────────────────────────────
//  Non-streaming: Anthropic Messages API (for compaction)
// ──────────────────────────────────────────────────────────────

type anthropicNonStreamResponse struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Content []anthropicContentBlock `json:"content"`
	Usage   *anthropicUsage         `json:"usage,omitempty"`
}

func nonStreamingChatAnthropic(client *LLMClient, messages []ChatMessage) (string, error) {
	system, anthMsgs := convertMessagesToAnthropic(messages)

	reqBody := anthropicRequest{
		Model:     client.Model,
		System:    system,
		Messages:  anthMsgs,
		MaxTokens: 4096,
		Stream:    false,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", client.BaseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", client.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := client.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(errBody))
	}

	var result anthropicNonStreamResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}

	// Extract text from content blocks
	var text strings.Builder
	for _, block := range result.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}

	if text.Len() == 0 {
		return "", fmt.Errorf("no text content in response")
	}

	return text.String(), nil
}
