package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Glue tests (unit tests that don't require an API)
// ──────────────────────────────────────────────────────────────

func TestNewGlueClientFallback(t *testing.T) {
	// When GLUE_* vars are not set, falls back to main config
	os.Unsetenv("GLUE_API_KEY")
	os.Unsetenv("GLUE_BASE_URL")
	os.Unsetenv("GLUE_FAST_MODEL")
	os.Unsetenv("GLUE_SMART_MODEL")

	cfg := &Config{
		APIKey:  "test-key",
		BaseURL: "https://api.example.com/v1",
		Model:   "gpt-4o",
	}

	glue := NewGlueClient(cfg)

	if glue.fast.APIKey != "test-key" {
		t.Errorf("fast client should fall back to main API key, got %s", glue.fast.APIKey)
	}
	if glue.fast.Model != "gpt-4o" {
		t.Errorf("fast model should fall back to main model, got %s", glue.fast.Model)
	}
	if glue.smart.Model != "gpt-4o" {
		t.Errorf("smart model should fall back to main model, got %s", glue.smart.Model)
	}
	if glue.IsConfigured() {
		t.Error("should not report as configured when no GLUE_* vars set")
	}
}

func TestNewGlueClientWithEnv(t *testing.T) {
	os.Setenv("GLUE_API_KEY", "glue-key")
	os.Setenv("GLUE_BASE_URL", "http://localhost:11434/v1")
	os.Setenv("GLUE_FAST_MODEL", "llama3.1:8b")
	os.Setenv("GLUE_SMART_MODEL", "qwen2.5:32b")
	defer func() {
		os.Unsetenv("GLUE_API_KEY")
		os.Unsetenv("GLUE_BASE_URL")
		os.Unsetenv("GLUE_FAST_MODEL")
		os.Unsetenv("GLUE_SMART_MODEL")
	}()

	cfg := &Config{
		APIKey:  "main-key",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o",
	}

	glue := NewGlueClient(cfg)

	if glue.fast.APIKey != "glue-key" {
		t.Errorf("fast client should use GLUE_API_KEY, got %s", glue.fast.APIKey)
	}
	if glue.fast.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("fast client should use GLUE_BASE_URL, got %s", glue.fast.BaseURL)
	}
	if glue.fast.Model != "llama3.1:8b" {
		t.Errorf("fast model should be llama3.1:8b, got %s", glue.fast.Model)
	}
	if glue.smart.Model != "qwen2.5:32b" {
		t.Errorf("smart model should be qwen2.5:32b, got %s", glue.smart.Model)
	}
	if !glue.IsConfigured() {
		t.Error("should report as configured when GLUE_* vars are set")
	}
}

func TestEnvOr(t *testing.T) {
	os.Setenv("TEST_ENVVAR_EXISTS", "hello")
	defer os.Unsetenv("TEST_ENVVAR_EXISTS")

	if v := envOr("TEST_ENVVAR_EXISTS", "fallback"); v != "hello" {
		t.Errorf("should return env value, got %s", v)
	}
	if v := envOr("TEST_ENVVAR_MISSING", "fallback"); v != "fallback" {
		t.Errorf("should return fallback, got %s", v)
	}
}

func TestNotificationTypes(t *testing.T) {
	// Verify all notification types have icons
	nm := newNotifyManager()
	types := []NotifyType{NotifyInfo, NotifyProgress, NotifySuccess, NotifyWarn, NotifyCelebrate}

	for _, nt := range types {
		icon := nm.icon(nt)
		if icon == "" {
			t.Errorf("notification type %d should have an icon", nt)
		}
	}
}

func TestNotificationExpiry(t *testing.T) {
	n := Notification{
		Type:      NotifyInfo,
		Text:      "test",
		CreatedAt: time.Now().Add(-10 * time.Second),
	}
	if !n.IsExpired() {
		t.Error("10-second-old info notification should be expired (default 3s)")
	}

	n2 := Notification{
		Type:      NotifyInfo,
		Text:      "test",
		CreatedAt: time.Now(),
	}
	if n2.IsExpired() {
		t.Error("fresh notification should not be expired")
	}
}

func TestNotifyManagerPush(t *testing.T) {
	nm := newNotifyManager()

	nm.Push(Notification{Type: NotifyInfo, Text: "first"})
	nm.Push(Notification{Type: NotifyInfo, Text: "second"})

	if len(nm.active) != 2 {
		t.Errorf("expected 2 active notifications, got %d", len(nm.active))
	}
}

func TestNotifyManagerProgressReplacement(t *testing.T) {
	nm := newNotifyManager()

	nm.Push(Notification{Type: NotifyProgress, Text: "reading files..."})
	nm.Push(Notification{Type: NotifyProgress, Text: "writing code..."})

	// Should have replaced, not stacked
	progressCount := 0
	for _, n := range nm.active {
		if n.Type == NotifyProgress {
			progressCount++
		}
	}
	if progressCount != 1 {
		t.Errorf("expected 1 progress notification (replaced), got %d", progressCount)
	}
	if nm.active[0].Text != "writing code..." {
		t.Errorf("expected replaced text, got %s", nm.active[0].Text)
	}
}

func TestNotifyManagerCap(t *testing.T) {
	nm := newNotifyManager()

	for i := 0; i < 10; i++ {
		nm.Push(Notification{Type: NotifyInfo, Text: "msg"})
	}

	if len(nm.active) > 4 {
		t.Errorf("should cap at 4 notifications, got %d", len(nm.active))
	}
}

func TestNotifyManagerTick(t *testing.T) {
	nm := newNotifyManager()

	// Add an expired notification
	nm.active = append(nm.active, Notification{
		Type:      NotifyInfo,
		Text:      "old",
		CreatedAt: time.Now().Add(-10 * time.Second),
	})
	// Add a fresh one
	nm.Push(Notification{Type: NotifyInfo, Text: "new"})

	nm.Tick()

	if len(nm.active) != 1 {
		t.Errorf("expected 1 active after tick (old expired), got %d", len(nm.active))
	}
	if nm.active[0].Text != "new" {
		t.Errorf("expected 'new' to survive, got %s", nm.active[0].Text)
	}
}

func TestNotifyManagerClearProgress(t *testing.T) {
	nm := newNotifyManager()

	nm.Push(Notification{Type: NotifyProgress, Text: "working..."})
	nm.Push(Notification{Type: NotifyInfo, Text: "info"})
	nm.Push(Notification{Type: NotifySuccess, Text: "done"})

	nm.ClearProgress()

	for _, n := range nm.active {
		if n.Type == NotifyProgress {
			t.Error("progress notifications should be cleared")
		}
	}
	if len(nm.active) != 2 {
		t.Errorf("expected 2 remaining notifications, got %d", len(nm.active))
	}
}

func TestNotifyManagerRender(t *testing.T) {
	nm := newNotifyManager()
	nm.Push(Notification{Type: NotifySuccess, Text: "Task complete"})

	rendered := nm.Render(80)
	if rendered == "" {
		t.Error("should render active notifications")
	}
	if !strings.Contains(rendered, "Task complete") {
		t.Error("rendered output should contain notification text")
	}
}

func TestNotifyManagerRenderEmpty(t *testing.T) {
	nm := newNotifyManager()

	rendered := nm.Render(80)
	if rendered != "" {
		t.Error("should return empty string when no active notifications")
	}
}

func TestRenderSuggestions(t *testing.T) {
	suggestions := []string{"Run tests", "Commit changes", "Add error handling"}
	rendered := renderSuggestions(suggestions, 80)

	if rendered == "" {
		t.Error("should render suggestions")
	}
	for _, s := range suggestions {
		if !strings.Contains(rendered, s) {
			t.Errorf("rendered suggestions should contain %q", s)
		}
	}
}

func TestRenderSuggestionsEmpty(t *testing.T) {
	rendered := renderSuggestions(nil, 80)
	if rendered != "" {
		t.Error("should return empty for nil suggestions")
	}
}

func TestDefaultDuration(t *testing.T) {
	cases := []struct {
		ntype    NotifyType
		expected time.Duration
	}{
		{NotifyInfo, 3 * time.Second},
		{NotifyProgress, 8 * time.Second},
		{NotifySuccess, 4 * time.Second},
		{NotifyWarn, 3 * time.Second},
		{NotifyCelebrate, 5 * time.Second},
	}

	for _, tc := range cases {
		n := Notification{Type: tc.ntype}
		if got := n.DefaultDuration(); got != tc.expected {
			t.Errorf("type %d: expected %v, got %v", tc.ntype, tc.expected, got)
		}
	}
}

func TestCustomDuration(t *testing.T) {
	n := Notification{
		Type:     NotifyInfo,
		Duration: 10 * time.Second,
	}
	if got := n.DefaultDuration(); got != 10*time.Second {
		t.Errorf("custom duration should be 10s, got %v", got)
	}
}

func TestSparkleFrame(t *testing.T) {
	nm := newNotifyManager()
	// Just verify it doesn't panic across several frames
	for i := 0; i < 20; i++ {
		nm.frame = i
		s := nm.sparkleFrame()
		if s == "" {
			t.Errorf("sparkle frame %d should not be empty", i)
		}
	}
}
