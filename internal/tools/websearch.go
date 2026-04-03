package tools

import (
	"context"
	"encoding/json"

	"github.com/codebase-foundation/cli/internal/tool"
)

// WebSearchFn is the function type for performing web searches.
// Set at registration time to avoid circular imports with search.go.
var WebSearchFn func(query string, maxResults int) (string, error)

type WebSearch struct{}

func (WebSearch) Name() string                          { return "web_search" }
func (WebSearch) ConcurrencySafe(_ map[string]any) bool { return true }
func (WebSearch) Effects() []tool.Effect                { return []tool.Effect{tool.EffectNetwork} }

func (WebSearch) Description() string {
	return "Search the web for current information. Use for: documentation, error solutions, " +
		"current versions, API references, or anything not available in local files."
}

func (WebSearch) Schema() json.RawMessage {
	return tool.MustSchema(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Search query."
			},
			"max_results": {
				"type": "number",
				"description": "Maximum number of results (default 5)."
			}
		},
		"required": ["query"]
	}`)
}

func (WebSearch) Execute(_ context.Context, args map[string]any, _ *tool.Env) tool.Result {
	query, _ := args["query"].(string)
	if query == "" {
		return tool.Result{Output: "Error: query is required", Success: false}
	}
	maxResults := 5
	if mr, ok := args["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}

	if WebSearchFn == nil {
		return tool.Result{Output: "Error: web search not configured", Success: false}
	}

	result, err := WebSearchFn(query, maxResults)
	if err != nil {
		return tool.Result{Output: "Search error: " + err.Error(), Success: false}
	}
	return tool.Result{Output: result, Success: true}
}
