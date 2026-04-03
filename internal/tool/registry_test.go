package tool

import (
	"context"
	"encoding/json"
	"testing"
)

// mockTool is a minimal Tool implementation for testing.
type mockTool struct {
	name        string
	description string
	schema      json.RawMessage
	effects     []Effect
	concurrent  bool
	execFn      func(ctx context.Context, args map[string]any, env *Env) Result
}

func (m mockTool) Name() string              { return m.name }
func (m mockTool) Description() string       { return m.description }
func (m mockTool) Schema() json.RawMessage   { return m.schema }
func (m mockTool) Effects() []Effect         { return m.effects }
func (m mockTool) ConcurrencySafe(_ map[string]any) bool { return m.concurrent }
func (m mockTool) Execute(ctx context.Context, args map[string]any, env *Env) Result {
	if m.execFn != nil {
		return m.execFn(ctx, args, env)
	}
	return Result{Output: "ok", Success: true}
}

func newMockTool(name string) mockTool {
	return mockTool{
		name:        name,
		description: "test tool: " + name,
		schema:      MustSchema(`{"type":"object","properties":{"msg":{"type":"string"}},"required":["msg"]}`),
		effects:     []Effect{EffectReadsFS},
		concurrent:  true,
	}
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("alpha"))
	r.Register(newMockTool("beta"))

	if r.Get("alpha") == nil {
		t.Fatal("expected to find alpha")
	}
	if r.Get("beta") == nil {
		t.Fatal("expected to find beta")
	}
	if r.Get("gamma") != nil {
		t.Fatal("expected nil for unregistered tool")
	}
}

func TestRegistry_Has(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("foo"))

	if !r.Has("foo") {
		t.Error("expected Has(foo) = true")
	}
	if r.Has("bar") {
		t.Error("expected Has(bar) = false")
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("dup"))

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register(newMockTool("dup"))
}

func TestRegistry_All_PreservesOrder(t *testing.T) {
	r := NewRegistry()
	names := []string{"charlie", "alpha", "bravo"}
	for _, n := range names {
		r.Register(newMockTool(n))
	}

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(all))
	}
	for i, n := range names {
		if all[i].Name() != n {
			t.Errorf("index %d: expected %q, got %q", i, n, all[i].Name())
		}
	}
}

func TestRegistry_Names(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("x"))
	r.Register(newMockTool("y"))

	got := r.Names()
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Errorf("unexpected names: %v", got)
	}
}

func TestRegistry_Execute_ValidatesArgs(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("test"))

	// Missing required field "msg"
	result := r.Execute(context.Background(), "test", map[string]any{}, &Env{WorkDir: "/tmp"})
	if result.Success {
		t.Fatal("expected validation failure")
	}
	if result.Output == "" {
		t.Fatal("expected error message")
	}
}

func TestRegistry_Execute_ValidArgs(t *testing.T) {
	r := NewRegistry()
	called := false
	m := newMockTool("test")
	m.execFn = func(_ context.Context, args map[string]any, _ *Env) Result {
		called = true
		return Result{Output: args["msg"].(string), Success: true}
	}
	r.Register(m)

	result := r.Execute(context.Background(), "test", map[string]any{"msg": "hello"}, &Env{WorkDir: "/tmp"})
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Output)
	}
	if !called {
		t.Fatal("execute function was not called")
	}
	if result.Output != "hello" {
		t.Errorf("expected output 'hello', got %q", result.Output)
	}
}

func TestRegistry_Execute_UnknownTool(t *testing.T) {
	r := NewRegistry()
	result := r.Execute(context.Background(), "nope", map[string]any{}, &Env{WorkDir: "/tmp"})
	if result.Success {
		t.Fatal("expected failure for unknown tool")
	}
}

func TestRegistry_OpenAITools(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("my_tool"))

	defs := r.OpenAITools()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(defs))
	}

	var parsed map[string]any
	if err := json.Unmarshal(defs[0], &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["type"] != "function" {
		t.Errorf("expected type=function, got %v", parsed["type"])
	}
	fn := parsed["function"].(map[string]any)
	if fn["name"] != "my_tool" {
		t.Errorf("expected name=my_tool, got %v", fn["name"])
	}
	if fn["parameters"] == nil {
		t.Error("expected parameters")
	}
}

func TestRegistry_AnthropicTools(t *testing.T) {
	r := NewRegistry()
	r.Register(newMockTool("my_tool"))

	defs := r.AnthropicTools()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool def, got %d", len(defs))
	}

	var parsed map[string]any
	if err := json.Unmarshal(defs[0], &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["name"] != "my_tool" {
		t.Errorf("expected name=my_tool, got %v", parsed["name"])
	}
	if parsed["input_schema"] == nil {
		t.Error("expected input_schema")
	}
	// Should NOT have "type":"function" wrapper
	if parsed["type"] != nil {
		t.Error("Anthropic format should not have type field")
	}
}

func TestMustSchema_ValidJSON(t *testing.T) {
	s := MustSchema(`{"type":"object"}`)
	if s == nil {
		t.Fatal("expected non-nil schema")
	}
}

func TestMustSchema_InvalidJSON_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on invalid JSON")
		}
	}()
	MustSchema(`{not json}`)
}
