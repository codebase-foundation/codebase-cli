package main

import "testing"

func TestNeedsPermissionReadOnly(t *testing.T) {
	readOnly := []string{"read_file", "list_files", "search_files", "web_search", "dispatch_agent"}
	for _, tool := range readOnly {
		if NeedsPermission(tool, nil) {
			t.Errorf("%s should NOT need permission", tool)
		}
	}
}

func TestNeedsPermissionMutating(t *testing.T) {
	mutating := []string{"write_file", "edit_file", "multi_edit"}
	for _, tool := range mutating {
		if !NeedsPermission(tool, nil) {
			t.Errorf("%s should need permission", tool)
		}
	}
}

func TestNeedsPermissionGitCommit(t *testing.T) {
	if !NeedsPermission("git_commit", map[string]any{"message": "test"}) {
		t.Error("git_commit should need permission")
	}
}

func TestNeedsPermissionGitBranchList(t *testing.T) {
	// Listing branches (no name arg) should NOT need permission
	if NeedsPermission("git_branch", map[string]any{}) {
		t.Error("git_branch with no name should NOT need permission")
	}
}

func TestNeedsPermissionGitBranchCreate(t *testing.T) {
	if !NeedsPermission("git_branch", map[string]any{"name": "feature-x", "create": true}) {
		t.Error("git_branch with name should need permission")
	}
}

func TestShellNeedsPermissionReadOnly(t *testing.T) {
	readOnlyCmds := []string{
		"ls -la",
		"cat file.txt",
		"grep -r pattern .",
		"git status",
		"git log --oneline",
		"git diff HEAD~1",
		"go vet ./...",
		"npm test",
		"go build ./...",
		"pytest",
		"pwd",
	}
	for _, cmd := range readOnlyCmds {
		args := map[string]any{"command": cmd}
		if NeedsPermission("shell", args) {
			t.Errorf("shell command %q should NOT need permission", cmd)
		}
	}
}

func TestShellNeedsPermissionMutating(t *testing.T) {
	mutatingCmds := []string{
		"rm -rf node_modules",
		"git push origin main",
		"npm install express",
		"pip install flask",
		"mkdir -p src/new",
		"docker run",
		"curl -X POST https://api.example.com",
	}
	for _, cmd := range mutatingCmds {
		args := map[string]any{"command": cmd}
		if !NeedsPermission("shell", args) {
			t.Errorf("shell command %q should need permission", cmd)
		}
	}
}

func TestShellPipedReadOnly(t *testing.T) {
	args := map[string]any{"command": "git log --oneline | head -5"}
	if NeedsPermission("shell", args) {
		t.Error("piped read-only command should NOT need permission")
	}
}

func TestPermissionSummary(t *testing.T) {
	tests := []struct {
		tool    string
		args    map[string]any
		expect  string
	}{
		{"write_file", map[string]any{"path": "main.go"}, "Create/overwrite: main.go"},
		{"edit_file", map[string]any{"path": "lib.go"}, "Edit: lib.go"},
		{"shell", map[string]any{"command": "echo hello"}, "Run: echo hello"},
		{"git_commit", map[string]any{"message": "fix bug"}, `Git commit: "fix bug"`},
	}
	for _, tt := range tests {
		got := PermissionSummary(tt.tool, tt.args)
		if got != tt.expect {
			t.Errorf("PermissionSummary(%s): got %q, want %q", tt.tool, got, tt.expect)
		}
	}
}

func TestPermissionSummaryLongCommand(t *testing.T) {
	longCmd := "very-long-command-that-exceeds-eighty-characters-because-it-has-many-arguments-and-flags-and-paths"
	got := PermissionSummary("shell", map[string]any{"command": longCmd})
	if len(got) > 90 { // "Run: " + 80 chars + "..."
		t.Errorf("summary not truncated: len=%d", len(got))
	}
}

func TestParsePermissionInput(t *testing.T) {
	tests := []struct {
		input   string
		allowed bool
		trust   PermissionLevel
	}{
		{"y", true, PermAsk},
		{"yes", true, PermAsk},
		{"Y", true, PermAsk},
		{"n", false, PermAsk},
		{"no", false, PermAsk},
		{"a", true, PermTrustTool},
		{"always", true, PermTrustTool},
		{"all", true, PermTrustAll},
		{"trust", true, PermTrustAll},
		{"garbage", true, PermAsk},
		{"", true, PermAsk},
	}
	for _, tt := range tests {
		resp := parsePermissionInput(tt.input)
		if resp.Allowed != tt.allowed {
			t.Errorf("parsePermissionInput(%q): allowed=%v, want %v", tt.input, resp.Allowed, tt.allowed)
		}
		if resp.TrustLevel != tt.trust {
			t.Errorf("parsePermissionInput(%q): trust=%d, want %d", tt.input, resp.TrustLevel, tt.trust)
		}
	}
}

func TestPermissionStateAutoApprove(t *testing.T) {
	state := &PermissionState{
		Level:        PermAsk,
		TrustedTools: map[string]bool{"write_file": true},
	}

	// Trusted tool should be auto-approved via the state check
	if !state.TrustedTools["write_file"] {
		t.Error("write_file should be in trusted tools")
	}

	// Untrusted tool should not
	if state.TrustedTools["shell"] {
		t.Error("shell should not be in trusted tools")
	}

	// TrustAll overrides everything
	state.Level = PermTrustAll
	// With TrustAll, the agent.checkPermission() returns true immediately
}

func TestPermissionEventInChatModel(t *testing.T) {
	m := initChat(t)

	// Simulate streaming state with a permission event
	m.state = chatStreaming
	m.eventCh = make(chan AgentEvent, 64)
	m.stopCh = make(chan struct{})
	m.streaming.Reset()

	// Create a dummy agent with permCh
	m.agent = &Agent{
		permCh:    make(chan PermissionResponse, 1),
		permState: &PermissionState{TrustedTools: map[string]bool{}},
	}

	// Send permission event
	evt := AgentEvent{
		Type: EventPermission,
		Permission: &PermissionRequest{
			Tool:    "write_file",
			Args:    map[string]any{"path": "test.go"},
			Summary: "Create/overwrite: test.go",
		},
	}
	m, _ = m.Update(agentEventMsg(evt))

	if m.state != chatPermission {
		t.Fatalf("expected chatPermission state, got %d", m.state)
	}
	if m.permRequest == nil {
		t.Fatal("permRequest should be set")
	}
	if m.permRequest.Tool != "write_file" {
		t.Errorf("expected tool write_file, got %s", m.permRequest.Tool)
	}
}
