package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codebase-foundation/cli/internal/tool"
)

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test Project\n\nA test project.\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "app.go"), []byte("package src\n\nfunc App() {}\n"), 0644)

	return dir
}

func TestReadFile_Basic(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{"path": "main.go"}, &tool.Env{WorkDir: dir})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Output)
	}
	if !strings.Contains(result.Output, "fmt.Println") {
		t.Error("expected file content")
	}
	if !strings.Contains(result.Output, "lines total") {
		t.Error("expected line count")
	}
	if len(result.Files) != 1 || result.Files[0] != "main.go" {
		t.Errorf("expected Files=[main.go], got %v", result.Files)
	}
}

func TestReadFile_WithOffset(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{
		"path":   "main.go",
		"offset": 3.0,
		"limit":  2.0,
	}, &tool.Env{WorkDir: dir})

	if !result.Success {
		t.Fatalf("expected success: %s", result.Output)
	}
	if !strings.Contains(result.Output, "Showing lines 3-4") {
		t.Errorf("expected range in output, got: %s", result.Output)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{"path": "nope.go"}, &tool.Env{WorkDir: dir})
	if result.Success {
		t.Fatal("expected failure")
	}
	if !strings.Contains(result.Output, "File not found") {
		t.Errorf("expected 'File not found', got: %s", result.Output)
	}
}

func TestReadFile_DirectoryError(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{"path": "src"}, &tool.Env{WorkDir: dir})
	if result.Success {
		t.Fatal("expected failure for directory")
	}
	if !strings.Contains(result.Output, "is a directory") {
		t.Errorf("expected directory error, got: %s", result.Output)
	}
}

func TestReadFile_PathTraversal(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{"path": "../../etc/passwd"}, &tool.Env{WorkDir: dir})
	if result.Success {
		t.Fatal("expected failure for path traversal")
	}
	if !strings.Contains(result.Output, "outside project root") {
		t.Errorf("expected traversal error, got: %s", result.Output)
	}
}

func TestReadFile_MissingPath(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{}, &tool.Env{WorkDir: dir})
	if result.Success {
		t.Fatal("expected failure for missing path")
	}
}

func TestReadFile_SubdirectoryFile(t *testing.T) {
	dir := setupTestDir(t)
	rf := ReadFile{}

	result := rf.Execute(context.Background(), map[string]any{"path": "src/app.go"}, &tool.Env{WorkDir: dir})
	if !result.Success {
		t.Fatalf("expected success: %s", result.Output)
	}
	if !strings.Contains(result.Output, "func App()") {
		t.Error("expected file content from subdirectory")
	}
}

func TestReadFile_ToolInterface(t *testing.T) {
	// Verify ReadFile satisfies the Tool interface
	var _ tool.Tool = ReadFile{}

	rf := ReadFile{}
	if rf.Name() != "read_file" {
		t.Errorf("expected name 'read_file', got %q", rf.Name())
	}
	if !rf.ConcurrencySafe(nil) {
		t.Error("read_file should be concurrency safe")
	}
	effects := rf.Effects()
	if len(effects) != 1 || effects[0] != tool.EffectReadsFS {
		t.Errorf("unexpected effects: %v", effects)
	}
	if rf.Schema() == nil {
		t.Error("expected non-nil schema")
	}
}

func TestReadFile_SchemaValidation(t *testing.T) {
	rf := ReadFile{}

	// Valid args
	if err := tool.ValidateArgs(rf.Schema(), map[string]any{"path": "foo.go"}); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}

	// Missing required
	if err := tool.ValidateArgs(rf.Schema(), map[string]any{}); err == nil {
		t.Error("expected error for missing path")
	}

	// Wrong type
	if err := tool.ValidateArgs(rf.Schema(), map[string]any{"path": 42.0}); err == nil {
		t.Error("expected error for wrong type")
	}
}
