// +build integration

package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codebase-foundation/cli/internal/tool"
)

func TestIntegration_ReadFile_RealFileSystem(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello, world!\")\n}\n",
		"go.mod":          "module example.com/calc\n\ngo 1.24\n",
		"calc/calc.go":    "package calc\n\nfunc Add(a, b int) int { return a + b }\n",
		"calc/calc_test.go": "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"2+3 should be 5\")\n\t}\n}\n",
	}
	for path, content := range files {
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0755)
		os.WriteFile(full, []byte(content), 0644)
	}

	reg := tool.NewRegistry()
	RegisterAll(reg)
	env := &tool.Env{WorkDir: dir}
	ctx := context.Background()

	t.Run("read_file via registry", func(t *testing.T) {
		result := reg.Execute(ctx, "read_file", map[string]any{"path": "main.go"}, env)
		if !result.Success {
			t.Fatalf("expected success: %s", result.Output)
		}
		if !strings.Contains(result.Output, "Hello, world!") {
			t.Error("expected file content")
		}
	})

	t.Run("read_file rejects bad args", func(t *testing.T) {
		result := reg.Execute(ctx, "read_file", map[string]any{"path": 42.0}, env)
		if result.Success {
			t.Fatal("expected failure for wrong type")
		}
		if !strings.Contains(result.Output, "invalid arguments") {
			t.Errorf("expected validation error, got: %s", result.Output)
		}
	})

	t.Run("read_file with offset", func(t *testing.T) {
		result := reg.Execute(ctx, "read_file", map[string]any{
			"path":   "calc/calc_test.go",
			"offset": 3.0,
			"limit":  3.0,
		}, env)
		if !result.Success {
			t.Fatalf("expected success: %s", result.Output)
		}
		if !strings.Contains(result.Output, "Showing lines") {
			t.Error("expected line range indicator")
		}
	})

	t.Run("read_file in subdirectory", func(t *testing.T) {
		result := reg.Execute(ctx, "read_file", map[string]any{"path": "calc/calc.go"}, env)
		if !result.Success {
			t.Fatalf("expected success: %s", result.Output)
		}
		if !strings.Contains(result.Output, "func Add") {
			t.Error("expected function definition")
		}
	})

	t.Run("blocks traversal", func(t *testing.T) {
		result := reg.Execute(ctx, "read_file", map[string]any{"path": "../../../etc/passwd"}, env)
		if result.Success {
			t.Fatal("expected failure for traversal")
		}
	})

	t.Run("unknown tool", func(t *testing.T) {
		result := reg.Execute(ctx, "not_a_tool", map[string]any{}, env)
		if result.Success {
			t.Fatal("expected failure for unknown tool")
		}
	})
}
