package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// ──────────────────────────────────────────────────────────────
//  Tool Registry
//
//  Central registry for all tools (local, MCP, subagent).
//  Handles registration, lookup, schema validation, and
//  provider-neutral schema generation.
// ──────────────────────────────────────────────────────────────

// Registry holds all registered tools and provides lookup + validation.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	order []string // insertion order for deterministic iteration
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry. Panics on duplicate names
// (caught at startup, not runtime).
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := t.Name()
	if _, exists := r.tools[name]; exists {
		panic(fmt.Sprintf("tool: duplicate registration for %q", name))
	}
	r.tools[name] = t
	r.order = append(r.order, name)
}

// Get returns a tool by name, or nil if not found.
func (r *Registry) Get(name string) Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tools[name]
}

// Has returns true if a tool with the given name is registered.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// All returns all tools in registration order.
func (r *Registry) All() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		result = append(result, r.tools[name])
	}
	return result
}

// Names returns all registered tool names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]string, len(r.order))
	copy(result, r.order)
	return result
}

// ──────────────────────────────────────────────────────────────
//  Validated execution
// ──────────────────────────────────────────────────────────────

// Execute validates args against the tool's schema, then runs it.
// Returns a Result with an error message if validation fails or tool is unknown.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any, env *Env) Result {
	t := r.Get(name)
	if t == nil {
		return Result{Output: fmt.Sprintf("Error: unknown tool %q", name), Success: false}
	}

	if err := ValidateArgs(t.Schema(), args); err != nil {
		return Result{
			Output:  fmt.Sprintf("Error: invalid arguments for %s: %s", name, err),
			Success: false,
		}
	}

	return t.Execute(ctx, args, env)
}

// ──────────────────────────────────────────────────────────────
//  Provider-neutral schema generation
//
//  Tools store schemas as plain JSON Schema objects. These methods
//  wrap them into the format each LLM provider expects.
// ──────────────────────────────────────────────────────────────

// OpenAITools returns tool definitions in OpenAI's function-calling format.
//
//	[{"type":"function","function":{"name":"...","description":"...","parameters":{...}}}]
func (r *Registry) OpenAITools() []json.RawMessage {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]json.RawMessage, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		def := openAIToolDef{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Schema(),
			},
		}
		raw, _ := json.Marshal(def)
		result = append(result, raw)
	}
	return result
}

// AnthropicTools returns tool definitions in Anthropic's Messages API format.
//
//	[{"name":"...","description":"...","input_schema":{...}}]
func (r *Registry) AnthropicTools() []json.RawMessage {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]json.RawMessage, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		def := anthropicToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Schema(),
		}
		raw, _ := json.Marshal(def)
		result = append(result, raw)
	}
	return result
}

// ──────────────────────────────────────────────────────────────
//  Provider schema wrappers (internal)
// ──────────────────────────────────────────────────────────────

type openAIToolDef struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type anthropicToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
