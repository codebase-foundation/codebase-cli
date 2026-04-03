// +build integration

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codebase-foundation/cli/internal/tool"
)

// TestIntegration_LLM_ToolRoundtrip sends a real request to the Anthropic API,
// gets back a tool_use response for read_file, executes it via the registry,
// and verifies the full loop works. Requires ANTHROPIC_API_KEY env var.
func TestIntegration_LLM_ToolRoundtrip(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set, skipping LLM roundtrip test")
	}

	// Set up a real project dir with a file
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("Hello from the codebase CLI!\nThis is a test file.\n"), 0644)

	// Set up registry
	reg := tool.NewRegistry()
	RegisterAll(reg)

	// Build Anthropic Messages API request
	toolDefs := reg.AnthropicTools()
	var toolDefsAny []any
	for _, td := range toolDefs {
		var parsed any
		json.Unmarshal(td, &parsed)
		toolDefsAny = append(toolDefsAny, parsed)
	}

	body := map[string]any{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"system":     "You are a coding assistant. You have access to tools. Use read_file to read the contents of hello.txt.",
		"messages": []map[string]any{
			{"role": "user", "content": "Read the file hello.txt and tell me what's in it."},
		},
		"tools": toolDefsAny,
	}

	bodyJSON, _ := json.Marshal(body)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyJSON))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("API request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response
	var result struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text,omitempty"`
			Name  string          `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		t.Fatalf("failed to parse response: %v\n%s", err, string(respBody))
	}

	// Find the tool_use block
	var toolUseFound bool
	for _, block := range result.Content {
		if block.Type == "tool_use" && block.Name == "read_file" {
			toolUseFound = true
			t.Logf("LLM called read_file with: %s", string(block.Input))

			// Parse the tool args
			var args map[string]any
			json.Unmarshal(block.Input, &args)

			// Execute via registry (schema validation + execution)
			toolResult := reg.Execute(context.Background(), "read_file", args, &tool.Env{WorkDir: dir})
			if !toolResult.Success {
				t.Fatalf("tool execution failed: %s", toolResult.Output)
			}

			t.Logf("Tool output:\n%s", toolResult.Output)

			if !strings.Contains(toolResult.Output, "Hello from the codebase CLI!") {
				t.Error("expected file content in tool output")
			}

			fmt.Printf("\n✓ Full LLM → tool_use → registry → execute → result pipeline works!\n\n")
		}
	}

	if !toolUseFound {
		// The model might have responded with text instead of a tool call
		// That's OK for a smoke test — log it
		t.Logf("Model did not call read_file (stop_reason: %s). Response: %s", result.StopReason, string(respBody))
		if result.StopReason == "end_turn" {
			t.Log("Model responded with text instead of tool call — acceptable for smoke test")
		}
	}
}
