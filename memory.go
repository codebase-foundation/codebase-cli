package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Cross-session memory system
//
//  Persistent, file-based memory at ~/.codebase/memory/ that
//  survives across sessions. Each memory is a markdown file with
//  YAML frontmatter (name, description, type). MEMORY.md is the
//  index — loaded into the system prompt so the model knows what
//  memories exist.
//
//  4 memory types:
//    user      — user's role, preferences, expertise
//    feedback  — corrections and validated approaches
//    project   — ongoing work, deadlines, decisions
//    reference — pointers to external systems
//
//  Better than CC: we organize per-project (keyed by workdir hash),
//  so memories from one project don't leak into another.
// ──────────────────────────────────────────────────────────────

const maxMemoryIndexLines = 200
const maxMemoryIndexBytes = 25 * 1024 // 25KB

// memoryDir returns the memory directory for the current project.
// Creates it if it doesn't exist.
func memoryDir(workDir string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	// Use project path hash for isolation between projects
	projectID := projectHash(workDir)
	dir := filepath.Join(home, ".codebase", "projects", projectID, "memory")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// projectHash creates a filesystem-safe identifier from a working directory.
func projectHash(workDir string) string {
	// Replace path separators and special chars with dashes
	safe := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		" ", "-",
	).Replace(workDir)
	// Strip leading dash
	safe = strings.TrimLeft(safe, "-")
	// Truncate if too long
	if len(safe) > 100 {
		safe = safe[len(safe)-100:]
	}
	return safe
}

// loadMemories reads all memory files and the MEMORY.md index.
// Returns content suitable for injection into the system prompt.
func loadMemories(workDir string) string {
	dir, err := memoryDir(workDir)
	if err != nil {
		return ""
	}

	// Read MEMORY.md index if it exists
	indexPath := filepath.Join(dir, "MEMORY.md")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return "" // no memories yet
	}

	content := string(indexData)

	// Enforce size limits
	if len(content) > maxMemoryIndexBytes {
		content = content[:maxMemoryIndexBytes] + "\n\n--- MEMORY INDEX TRUNCATED ---"
	}

	lines := strings.Split(content, "\n")
	if len(lines) > maxMemoryIndexLines {
		content = strings.Join(lines[:maxMemoryIndexLines], "\n") + "\n\n--- MEMORY INDEX TRUNCATED (200 line limit) ---"
	}

	if strings.TrimSpace(content) == "" {
		return ""
	}

	return content
}

// loadMemoryFile reads a specific memory file's content.
// Used when the model needs to access a memory referenced in the index.
func loadMemoryFile(workDir, filename string) (string, error) {
	dir, err := memoryDir(workDir)
	if err != nil {
		return "", err
	}

	// Safety: prevent path traversal
	clean := filepath.Clean(filename)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid memory filename: %s", filename)
	}

	data, err := os.ReadFile(filepath.Join(dir, clean))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// saveMemoryFile writes a memory file and updates the index.
func saveMemoryFile(workDir, filename, content string) error {
	dir, err := memoryDir(workDir)
	if err != nil {
		return err
	}

	clean := filepath.Clean(filename)
	if strings.Contains(clean, "..") || filepath.IsAbs(clean) {
		return fmt.Errorf("invalid memory filename: %s", filename)
	}

	return os.WriteFile(filepath.Join(dir, clean), []byte(content), 0644)
}

// injectMemoryContext adds memory content to the system prompt.
// Called during system prompt assembly.
func injectMemoryContext(sb *strings.Builder, workDir string) {
	memories := loadMemories(workDir)
	if memories == "" {
		return
	}

	sb.WriteString("\n\n# Memory (from previous sessions)\n\n")
	sb.WriteString("The following is your memory index from previous sessions with this project. ")
	sb.WriteString("Use this context to understand the user, their preferences, and ongoing work. ")
	sb.WriteString("Memory files can be read for full details.\n\n")
	sb.WriteString(memories)
	sb.WriteString("\n")
}
