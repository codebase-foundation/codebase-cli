package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Glue — fast/cheap LLM sidecar for intent routing,
//  narration, notifications, and presentation tasks.
//
//  Mirrors the web app's Glue layer. Uses separate env vars
//  so users can point at a local model (ollama, llama.cpp, etc.)
//  while keeping the main agent on a smarter model.
//
//  Env vars (all optional — falls back to OPENAI_* equivalents):
//    GLUE_API_KEY       (default: OPENAI_API_KEY)
//    GLUE_BASE_URL      (default: OPENAI_BASE_URL)
//    GLUE_FAST_MODEL    (default: OPENAI_MODEL)
//    GLUE_SMART_MODEL   (default: OPENAI_MODEL)
// ──────────────────────────────────────────────────────────────

// GlueClient provides fast/cheap LLM calls for routing and presentation.
type GlueClient struct {
	fast  *LLMClient // narration, acks, titles
	smart *LLMClient // intent classification, question answering
}

// NewGlueClient creates a Glue sidecar from env vars, falling back to main config.
func NewGlueClient(mainCfg *Config) *GlueClient {
	apiKey := envOr("GLUE_API_KEY", mainCfg.APIKey)
	baseURL := envOr("GLUE_BASE_URL", mainCfg.BaseURL)
	fastModel := envOr("GLUE_FAST_MODEL", mainCfg.Model)
	smartModel := envOr("GLUE_SMART_MODEL", mainCfg.Model)

	return &GlueClient{
		fast:  NewLLMClient(apiKey, baseURL, fastModel),
		smart: NewLLMClient(apiKey, baseURL, smartModel),
	}
}

// IsConfigured returns true if glue has dedicated model config
// (not just falling back to the main agent model).
func (g *GlueClient) IsConfigured() bool {
	return os.Getenv("GLUE_FAST_MODEL") != "" || os.Getenv("GLUE_SMART_MODEL") != ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// ──────────────────────────────────────────────────────────────
//  Intent Classification
// ──────────────────────────────────────────────────────────────

// Intent is the classified type of a user message.
type Intent string

const (
	IntentAgent   Intent = "agent"   // simple task — straight to agent loop
	IntentPlan    Intent = "plan"    // complex task — needs Q&A planning first
	IntentChat    Intent = "chat"    // simple question/conversation — answer directly
	IntentClarify Intent = "clarify" // ambiguous — ask for clarification
)

const classifyPrompt = `Classify this user message into exactly ONE category:

- "plan": User wants to BUILD something complex, multi-step, or architectural. Needs clarifying questions before starting. Examples: new features, new projects, major refactors, system design.
- "agent": User wants a SIMPLE, CLEAR change: fix a specific bug, run a command, edit a known file, add something small and well-defined. Can start immediately without planning.
- "chat": User is asking a QUESTION, making conversation, greeting, giving feedback, or asking about capabilities. No file changes needed.
- "clarify": User's request is too vague to act on. They said something like "fix it" or "make it better" without enough context.

Key distinction — plan vs agent:
- "build an auth system" → plan (complex, many decisions to make)
- "add dark mode to the app" → plan (design decisions, multiple files)
- "create a REST API for users" → plan (endpoints, schema, middleware)
- "fix the typo in main.go" → agent (simple, clear)
- "run the tests" → agent (no planning needed)
- "add a .gitignore file" → agent (straightforward)
- "rename the function to camelCase" → agent (simple edit)
- "refactor the auth module to use JWT" → plan (architectural change)

Respond with ONLY the category word: plan, agent, chat, or clarify`

// ClassifyIntent determines what kind of response a user message needs.
func (g *GlueClient) ClassifyIntent(userMsg string, hasHistory bool) Intent {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(classifyPrompt)},
		{Role: "user", Content: strPtr(userMsg)},
	}

	result, err := nonStreamingChat(g.smart, messages)
	if err != nil {
		return IntentAgent // fail open — always let the agent handle it
	}

	result = strings.TrimSpace(strings.ToLower(result))
	// Extract just the keyword — check plan before agent since "agent" is a substring match risk
	for _, intent := range []Intent{IntentPlan, IntentChat, IntentClarify, IntentAgent} {
		if strings.Contains(result, string(intent)) {
			// If they have conversation history, "clarify" becomes less useful
			if intent == IntentClarify && hasHistory {
				return IntentAgent
			}
			return intent
		}
	}
	return IntentAgent // default: let the agent handle it
}

// ──────────────────────────────────────────────────────────────
//  Chat Reply (fast model, no agent loop)
// ──────────────────────────────────────────────────────────────

const chatSystemPrompt = `You are Codebase, a local AI coding assistant running in the user's terminal.
Answer briefly (1-3 sentences). Be helpful and direct. If the user is greeting you, be friendly.
If they ask something that would require reading/modifying files, tell them to describe the task and you'll handle it.`

// ChatReply generates a quick response for non-agent messages.
func (g *GlueClient) ChatReply(userMsg string, recentContext []ChatMessage) string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(chatSystemPrompt)},
	}
	// Include recent context for multi-turn chat
	for _, msg := range recentContext {
		if msg.Role == "user" || msg.Role == "assistant" {
			messages = append(messages, ChatMessage{Role: msg.Role, Content: msg.Content})
		}
	}
	messages = append(messages, ChatMessage{Role: "user", Content: strPtr(userMsg)})

	result, err := nonStreamingChat(g.fast, messages)
	if err != nil {
		return "Hey! Describe what you'd like to build or change, and I'll get to work."
	}
	return strings.TrimSpace(result)
}

// ──────────────────────────────────────────────────────────────
//  Clarify Reply
// ──────────────────────────────────────────────────────────────

const clarifySystemPrompt = `The user gave a vague coding request. Ask ONE short clarifying question (1 sentence) to understand what they want. Be specific about what information you need.`

// ClarifyReply asks for more detail when intent is ambiguous.
func (g *GlueClient) ClarifyReply(userMsg string) string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(clarifySystemPrompt)},
		{Role: "user", Content: strPtr(userMsg)},
	}

	result, err := nonStreamingChat(g.fast, messages)
	if err != nil {
		return "Could you be more specific about what you'd like me to do?"
	}
	return strings.TrimSpace(result)
}

// ──────────────────────────────────────────────────────────────
//  Session Title Generation
// ──────────────────────────────────────────────────────────────

const titlePrompt = `Generate a very short title (2-5 words, no quotes) for this coding session based on the user's request. Examples: "Auth system refactor", "Add dark mode", "Fix database queries", "API endpoint tests"`

// GenerateTitle creates a short session title from the user's first prompt.
func (g *GlueClient) GenerateTitle(userMsg string) string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(titlePrompt)},
		{Role: "user", Content: strPtr(userMsg)},
	}

	result, err := nonStreamingChat(g.fast, messages)
	if err != nil {
		return ""
	}
	title := strings.TrimSpace(result)
	title = strings.Trim(title, "\"'")
	if len(title) > 50 {
		title = title[:47] + "..."
	}
	return title
}

// ──────────────────────────────────────────────────────────────
//  Narration (progress updates during agent work)
// ──────────────────────────────────────────────────────────────

const narratePrompt = `You are narrating an AI coding agent's progress. Given the recent tool actions, write a SHORT progress update (5-10 words). Be specific about what's happening. Use present continuous tense.

Examples of good narration:
- "Reading the authentication middleware..."
- "Searching for database connection code..."
- "Writing new test cases for the API..."
- "Running the test suite..."
- "Editing the user model to add validation..."

Do NOT use quotes. Just the narration text.`

// Narrate generates a short progress message based on recent agent activity.
func (g *GlueClient) Narrate(recentActions []string) string {
	if len(recentActions) == 0 {
		return ""
	}

	context := strings.Join(recentActions, "\n")
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(narratePrompt)},
		{Role: "user", Content: strPtr("Recent agent actions:\n" + context)},
	}

	result, err := nonStreamingChat(g.fast, messages)
	if err != nil {
		return ""
	}
	narration := strings.TrimSpace(result)
	narration = strings.Trim(narration, "\"'")
	if len(narration) > 80 {
		narration = narration[:77] + "..."
	}
	return narration
}

// ──────────────────────────────────────────────────────────────
//  Completion Celebration
// ──────────────────────────────────────────────────────────────

const celebratePrompt = `The AI coding agent just finished a task. Write a SHORT celebration message (5-12 words). Be enthusiastic but not cringy. Reference what was done if possible.

Examples:
- "Auth system is live and tested!"
- "All tests passing — nice and clean."
- "Dark mode implemented across all pages."
- "Bug squashed, API responses are fast now."
- "Refactoring done — much cleaner architecture."

Do NOT use quotes or emoji. Just the message.`

// Celebrate generates a short celebration message when a task completes.
func (g *GlueClient) Celebrate(summary string) string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(celebratePrompt)},
		{Role: "user", Content: strPtr("Task summary: " + summary)},
	}

	result, err := nonStreamingChat(g.fast, messages)
	if err != nil {
		return "Done!"
	}
	msg := strings.TrimSpace(result)
	msg = strings.Trim(msg, "\"'")
	if len(msg) > 80 {
		msg = msg[:77] + "..."
	}
	return msg
}

// ──────────────────────────────────────────────────────────────
//  Suggest Follow-Ups
// ──────────────────────────────────────────────────────────────

const suggestPrompt = `After completing a coding task, suggest 2-3 brief follow-up actions the user might want. Each should be a short imperative phrase (3-8 words).

Respond as JSON: {"suggestions": ["Run the test suite", "Add error handling", "Commit the changes"]}

Only suggest things that make sense given what was just done.`

// SuggestFollowUps returns 2-3 follow-up action suggestions.
func (g *GlueClient) SuggestFollowUps(taskSummary string, filesChanged int) []string {
	messages := []ChatMessage{
		{Role: "system", Content: strPtr(suggestPrompt)},
		{Role: "user", Content: strPtr(fmt.Sprintf("Task: %s\nFiles changed: %d", taskSummary, filesChanged))},
	}

	result, err := nonStreamingChat(g.smart, messages)
	if err != nil {
		return nil
	}

	// Parse JSON response
	var parsed struct {
		Suggestions []string `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		// Try to extract from messy response
		result = strings.TrimSpace(result)
		if idx := strings.Index(result, "{"); idx >= 0 {
			if end := strings.LastIndex(result, "}"); end > idx {
				json.Unmarshal([]byte(result[idx:end+1]), &parsed)
			}
		}
	}

	if len(parsed.Suggestions) > 3 {
		parsed.Suggestions = parsed.Suggestions[:3]
	}
	return parsed.Suggestions
}

// ──────────────────────────────────────────────────────────────
//  Notification Directives (animation control)
// ──────────────────────────────────────────────────────────────

// NotifyType controls the visual style of a TUI notification.
type NotifyType int

const (
	NotifyInfo      NotifyType = iota // blue — general info
	NotifyProgress                    // cyan — work in progress
	NotifySuccess                     // green — task completed
	NotifyWarn                        // yellow — heads up
	NotifyCelebrate                   // purple+sparkle — celebration
)

// Notification is a toast message with animation metadata.
type Notification struct {
	Type      NotifyType
	Text      string
	Duration  time.Duration // how long to show (0 = default 3s)
	CreatedAt time.Time
}

// DefaultDuration returns the display duration for a notification type.
func (n Notification) DefaultDuration() time.Duration {
	if n.Duration > 0 {
		return n.Duration
	}
	switch n.Type {
	case NotifyCelebrate:
		return 5 * time.Second
	case NotifySuccess:
		return 4 * time.Second
	case NotifyProgress:
		return 8 * time.Second // progress stays longer, replaced by next one
	default:
		return 3 * time.Second
	}
}

// IsExpired returns true if the notification should be dismissed.
func (n Notification) IsExpired() bool {
	return time.Since(n.CreatedAt) > n.DefaultDuration()
}
