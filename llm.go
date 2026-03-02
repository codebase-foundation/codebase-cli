package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  OpenAI Chat Completions types
// ──────────────────────────────────────────────────────────────

type ChatMessage struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDef struct {
	Type     string         `json:"type"`
	Function ToolDefFunction `json:"function"`
}

type ToolDefFunction struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Parameters  interface{} `json:"parameters"`
}

// ──────────────────────────────────────────────────────────────
//  Streaming chunk types
// ──────────────────────────────────────────────────────────────

type StreamChunk struct {
	Choices []StreamChoice `json:"choices"`
	Usage   *ChunkUsage    `json:"usage,omitempty"`
}

type StreamChoice struct {
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

type StreamDelta struct {
	Content   *string          `json:"content,omitempty"`
	ToolCalls []ToolCallDelta  `json:"tool_calls,omitempty"`
}

type ToolCallDelta struct {
	Index    *int              `json:"index,omitempty"`
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type,omitempty"`
	Function *FunctionCallDelta `json:"function,omitempty"`
}

type FunctionCallDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type ChunkUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// ──────────────────────────────────────────────────────────────
//  Parsed stream events (sent to agent loop)
// ──────────────────────────────────────────────────────────────

type StreamEventType int

const (
	StreamText      StreamEventType = iota // text content delta
	StreamToolCalls                        // complete, accumulated tool calls
	StreamUsage                            // token usage
	StreamDone                             // stream finished
	StreamError                            // error
)

type StreamEvent struct {
	Type      StreamEventType
	Text      string
	ToolCalls []ToolCall
	Usage     ChunkUsage
	Error     error
}

// ──────────────────────────────────────────────────────────────
//  LLM Client
// ──────────────────────────────────────────────────────────────

// Protocol constants for API format detection
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

type LLMClient struct {
	APIKey   string
	BaseURL  string
	Model    string
	Protocol string // "openai" or "anthropic"
	client   *http.Client
}

func NewLLMClient(apiKey, baseURL, model string) *LLMClient {
	return newLLMClientWithTimeout(apiKey, baseURL, model, 5*time.Minute)
}

func newLLMClientWithTimeout(apiKey, baseURL, model string, timeout time.Duration) *LLMClient {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	if model == "" {
		model = "gpt-4o"
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	protocol := detectProtocol(baseURL)

	return &LLMClient{
		APIKey:   apiKey,
		BaseURL:  baseURL,
		Model:    model,
		Protocol: protocol,
		client:   &http.Client{Timeout: timeout},
	}
}

// detectProtocol determines the API protocol from the base URL and env vars.
func detectProtocol(baseURL string) string {
	// Explicit env var override
	if p := os.Getenv("LLM_PROTOCOL"); p != "" {
		switch strings.ToLower(p) {
		case "anthropic", "claude", "messages":
			return ProtocolAnthropic
		default:
			return ProtocolOpenAI
		}
	}
	// Auto-detect from URL
	urlLower := strings.ToLower(baseURL)
	if strings.Contains(urlLower, "/anthropic") || strings.Contains(urlLower, "anthropic.com") {
		return ProtocolAnthropic
	}
	return ProtocolOpenAI
}

// StreamChat sends a streaming LLM request and pushes parsed events
// into the provided channel. Dispatches to the appropriate protocol
// implementation based on c.Protocol.
func (c *LLMClient) StreamChat(ctx context.Context, messages []ChatMessage, tools []ToolDef, ch chan<- StreamEvent) {
	if c.Protocol == ProtocolAnthropic {
		c.streamChatAnthropic(ctx, messages, tools, ch)
		return
	}
	c.streamChatOpenAI(ctx, messages, tools, ch)
}

// streamChatOpenAI sends a streaming OpenAI Chat Completions request.
func (c *LLMClient) streamChatOpenAI(ctx context.Context, messages []ChatMessage, tools []ToolDef, ch chan<- StreamEvent) {
	defer close(ch)

	body := map[string]interface{}{
		"model":    c.Model,
		"messages": messages,
		"stream":   true,
		"stream_options": map[string]interface{}{
			"include_usage": true,
		},
	}
	if len(tools) > 0 {
		body["tools"] = tools
		body["tool_choice"] = "auto"
		body["parallel_tool_calls"] = true
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("marshal: %w", err)}
		return
	}

	// Retry transient errors (429, 502, 503) up to 3 times with backoff
	var resp *http.Response
	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/chat/completions", bytes.NewReader(jsonBody))
		if err != nil {
			ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("request: %w", err)}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Accept", "text/event-stream")

		resp, err = c.client.Do(req)
		if err != nil {
			ch <- StreamEvent{Type: StreamError, Error: fmt.Errorf("connection error: %v", err)}
			return
		}

		// Retry on transient HTTP errors
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

	// Parse SSE stream
	var accumulated []ToolCall
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // skip malformed chunks
		}

		// Usage (usually in the final chunk)
		if chunk.Usage != nil {
			ch <- StreamEvent{Type: StreamUsage, Usage: *chunk.Usage}
		}

		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		delta := choice.Delta

		// Text content
		if delta.Content != nil && *delta.Content != "" {
			ch <- StreamEvent{Type: StreamText, Text: *delta.Content}
		}

		// Tool call deltas — accumulate progressively
		for i, tcd := range delta.ToolCalls {
			idx := i
			if tcd.Index != nil {
				idx = *tcd.Index
			}
			// Grow the slice
			for len(accumulated) <= idx {
				accumulated = append(accumulated, ToolCall{Type: "function"})
			}
			if tcd.ID != "" {
				accumulated[idx].ID = tcd.ID
			}
			if tcd.Function != nil {
				if tcd.Function.Name != "" {
					accumulated[idx].Function.Name = tcd.Function.Name
				}
				accumulated[idx].Function.Arguments += tcd.Function.Arguments
			}
		}

		// Check for finish
		if choice.FinishReason != nil {
			if len(accumulated) > 0 {
				// Emit tool calls on any finish reason — some providers
				// use "stop" instead of "tool_calls"
				ch <- StreamEvent{Type: StreamToolCalls, ToolCalls: accumulated}
				accumulated = nil
			}
		}
	}

	ch <- StreamEvent{Type: StreamDone}
}

// truncateErrorBody shortens raw API error bodies for display.
func truncateErrorBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// ──────────────────────────────────────────────────────────────
//  Model listing — query provider APIs for available models
// ──────────────────────────────────────────────────────────────

// listModels fetches available model IDs from an API provider.
// protocol should be "openai" or "anthropic".
func listModels(apiKey, baseURL, protocol string) ([]string, error) {
	baseURL = strings.TrimSuffix(baseURL, "/")
	client := &http.Client{Timeout: 15 * time.Second}

	var req *http.Request
	var err error

	if protocol == ProtocolAnthropic {
		req, err = http.NewRequest("GET", baseURL+"/v1/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req, err = http.NewRequest("GET", baseURL+"/models", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connection error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, truncateErrorBody(string(body)))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode error: %v", err)
	}

	// Filter: keep only chat/completion models, skip embeddings/whisper/dall-e/tts
	skipPrefixes := []string{"embedding", "text-embedding", "whisper", "dall-e", "tts", "davinci", "babbage", "ada"}
	skipContains := []string{"embed", "whisper", "dall-e", "tts-", "moderation", "realtime"}

	var models []string
	for _, m := range result.Data {
		id := strings.ToLower(m.ID)
		skip := false
		for _, p := range skipPrefixes {
			if strings.HasPrefix(id, p) {
				skip = true
				break
			}
		}
		if !skip {
			for _, c := range skipContains {
				if strings.Contains(id, c) {
					skip = true
					break
				}
			}
		}
		if !skip {
			models = append(models, m.ID)
		}
	}

	sort.Strings(models)
	return models, nil
}

// humanizeError converts raw Go/API errors into user-friendly messages.
func humanizeError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return "Cannot connect to the API server. Check your OPENAI_BASE_URL."
	case strings.Contains(msg, "no such host"):
		return "DNS resolution failed. Check your network connection and OPENAI_BASE_URL."
	case strings.Contains(msg, "context deadline exceeded"),
		strings.Contains(msg, "Client.Timeout"):
		return "Request timed out. The API server may be overloaded."
	case strings.Contains(msg, "API error 401"):
		return "Authentication failed. Check your OPENAI_API_KEY."
	case strings.Contains(msg, "API error 403"):
		return "Access denied. Your API key may not have permission for this model."
	case strings.Contains(msg, "API error 404"):
		return "Model not found. Check your model name."
	case strings.Contains(msg, "API error 429"):
		return "Rate limited. Too many requests — please wait a moment."
	default:
		// Cap length for display
		if len(msg) > 200 {
			return msg[:200] + "..."
		}
		return msg
	}
}

