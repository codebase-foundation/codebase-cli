package main

import (
	"context"
	"encoding/json"

	"github.com/codebase-foundation/cli/internal/tool"
	"github.com/codebase-foundation/cli/internal/tools"
)

// ──────────────────────────────────────────────────────────────
//  Global tool registry
//
//  All tools are registered here. The old toolDefs/ExecuteTool
//  system is gone — everything goes through the registry.
// ──────────────────────────────────────────────────────────────

var globalRegistry *tool.Registry
var globalMCP *MCPManager

func init() {
	globalRegistry = tool.NewRegistry()
	tools.RegisterAll(globalRegistry)

	// Wire external functions that tools need
	tools.MemoryDirFn = memoryDir
	tools.WebSearchFn = func(query string, maxResults int) (string, error) {
		resp, err := WebSearch(query, maxResults)
		if err != nil {
			return "", err
		}
		return FormatSearchResults(resp), nil
	}

	// Connect to MCP servers (non-blocking — failures are logged, not fatal)
	globalMCP = NewMCPManager()
	globalMCP.LoadAndConnect(globalRegistry)
}

// allToolDefs returns tool definitions for the LLM in OpenAI format.
// The Anthropic adapter handles conversion at the protocol layer.
func allToolDefs() []ToolDef {
	var defs []ToolDef
	for _, raw := range globalRegistry.OpenAITools() {
		var td ToolDef
		json.Unmarshal(raw, &td)
		defs = append(defs, td)
	}
	return defs
}

// registryExecute runs a tool through the registry with validation.
func registryExecute(ctx context.Context, name string, args map[string]any, env *tool.Env) tool.Result {
	return globalRegistry.Execute(ctx, name, args, env)
}

// registryIsParallelSafe checks if a specific tool invocation is parallel-safe.
func registryIsParallelSafe(name string, args map[string]any) bool {
	if t := globalRegistry.Get(name); t != nil {
		return t.ConcurrencySafe(args)
	}
	return false // unknown tool = not safe
}
