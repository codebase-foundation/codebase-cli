package main

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/codebase-foundation/cli/internal/tool"
)

// ──────────────────────────────────────────────────────────────
//  Streaming Tool Executor
//
//  Executes tools as they arrive during streaming, before the
//  full LLM response completes. Concurrency-safe tools start
//  immediately and run in parallel. Non-safe tools queue and
//  wait for prior work to complete.
//
//  This sits between the stream consumer and the batch executor.
//  When streaming is enabled, the agent loop feeds tool calls
//  into the executor as they complete, instead of waiting for
//  the full response.
//
//  Better than CC: simpler (no abort cascading, no sibling kill),
//  same benefit (faster overall execution).
// ──────────────────────────────────────────────────────────────

// StreamingExecutor manages concurrent tool execution during streaming.
type StreamingExecutor struct {
	mu          sync.Mutex
	env         *tool.Env
	results     []streamingResult
	pending     []ToolCall       // tools waiting to execute
	executing   int              // count of currently executing tools
	allSafe     bool             // true if all executing tools are concurrency-safe
	events      chan<- AgentEvent
	done        chan struct{}     // closed when all tools complete
	wg          sync.WaitGroup
}

type streamingResult struct {
	tc      ToolCall
	args    map[string]any
	output  string
	success bool
	order   int // insertion order for deterministic output
}

// NewStreamingExecutor creates an executor for streaming tool execution.
func NewStreamingExecutor(env *tool.Env, events chan<- AgentEvent) *StreamingExecutor {
	return &StreamingExecutor{
		env:    env,
		events: events,
		done:   make(chan struct{}),
	}
}

// Submit adds a tool call to the executor. If it's concurrency-safe and
// no non-safe tools are running, it starts immediately. Otherwise it queues.
func (se *StreamingExecutor) Submit(tc ToolCall) {
	var argsMap map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &argsMap); err != nil {
		argsMap = map[string]any{"_raw": tc.Function.Arguments}
	}

	safe := registryIsParallelSafe(tc.Function.Name, argsMap)
	order := len(se.results) + len(se.pending)

	se.mu.Lock()
	canRun := se.executing == 0 || (safe && se.allSafe)

	if canRun {
		se.executing++
		if se.executing == 1 {
			se.allSafe = safe
		} else {
			se.allSafe = se.allSafe && safe
		}
		se.mu.Unlock()

		// Fire event and start executing
		se.events <- AgentEvent{
			Type: EventToolStart, Tool: tc.Function.Name, ToolID: tc.ID, Args: argsMap,
		}

		se.wg.Add(1)
		go se.execute(tc, argsMap, safe, order)
	} else {
		// Queue for later
		se.pending = append(se.pending, tc)
		se.mu.Unlock()
	}
}

// execute runs a single tool and stores the result.
func (se *StreamingExecutor) execute(tc ToolCall, args map[string]any, safe bool, order int) {
	defer se.wg.Done()

	result := registryExecute(context.Background(), tc.Function.Name, args, se.env)
	output := maybePersistToolResult(tc.Function.Name, result.Output)

	se.mu.Lock()
	se.results = append(se.results, streamingResult{
		tc: tc, args: args, output: output, success: result.Success, order: order,
	})
	se.executing--

	// Try to dequeue pending tools
	se.tryDequeue()
	se.mu.Unlock()

	// Emit result event
	se.events <- AgentEvent{
		Type: EventToolResult, Tool: tc.Function.Name, ToolID: tc.ID,
		Args: args, Output: output, Success: result.Success,
	}
}

// tryDequeue starts queued tools if possible. Must be called with mu held.
func (se *StreamingExecutor) tryDequeue() {
	for len(se.pending) > 0 {
		tc := se.pending[0]
		var argsMap map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)
		safe := registryIsParallelSafe(tc.Function.Name, argsMap)

		canRun := se.executing == 0 || (safe && se.allSafe)
		if !canRun {
			break
		}

		se.pending = se.pending[1:]
		se.executing++
		if se.executing == 1 {
			se.allSafe = safe
		} else {
			se.allSafe = se.allSafe && safe
		}

		order := len(se.results) + len(se.pending)
		se.events <- AgentEvent{
			Type: EventToolStart, Tool: tc.Function.Name, ToolID: tc.ID, Args: argsMap,
		}

		se.wg.Add(1)
		go se.execute(tc, argsMap, safe, order)
	}
}

// Wait blocks until all submitted tools have completed.
func (se *StreamingExecutor) Wait() {
	se.wg.Wait()
}

// Results returns all tool results. Call after Wait().
func (se *StreamingExecutor) Results() []streamingResult {
	se.mu.Lock()
	defer se.mu.Unlock()
	return se.results
}

// HasErrors returns true if all results are errors.
func (se *StreamingExecutor) HasErrors() bool {
	se.mu.Lock()
	defer se.mu.Unlock()
	for _, r := range se.results {
		if r.success {
			return false
		}
	}
	return len(se.results) > 0
}
