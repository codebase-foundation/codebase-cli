package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Tool Result Persistence
//
//  When a tool output exceeds the size threshold, the full result
//  is saved to disk and the model receives a preview + file path.
//  The model can read_file the full result if needed.
//
//  CC does this in toolResultStorage.ts with a 50KB default.
//  Ours is simpler: single threshold, no per-tool overrides,
//  preview = first 2000 chars + "... [full result saved to X]".
// ──────────────────────────────────────────────────────────────

const (
	toolResultMaxChars    = 50000 // 50KB — save to disk above this
	toolResultPreviewLen  = 2000  // preview size for model
)

// toolResultsDir returns the path for persisted tool results, creating if needed.
func toolResultsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".codebase", "tool-results")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// maybePersistToolResult checks if output is too large and persists if so.
// Returns the (possibly truncated) output for the model.
func maybePersistToolResult(toolName, output string) string {
	if len(output) <= toolResultMaxChars {
		return output
	}

	dir, err := toolResultsDir()
	if err != nil {
		// Can't persist — truncate with notice
		return output[:toolResultMaxChars] + fmt.Sprintf("\n\n--- TRUNCATED (%d chars total, persistence failed: %v) ---", len(output), err)
	}

	// Generate filename from content hash
	h := sha256.Sum256([]byte(output))
	hash := hex.EncodeToString(h[:8])
	filename := fmt.Sprintf("%s-%s.txt", toolName, hash)
	fullPath := filepath.Join(dir, filename)

	// Write full result to disk
	if err := os.WriteFile(fullPath, []byte(output), 0644); err != nil {
		return output[:toolResultMaxChars] + fmt.Sprintf("\n\n--- TRUNCATED (%d chars total, write failed: %v) ---", len(output), err)
	}

	// Build preview
	preview := output
	if len(preview) > toolResultPreviewLen {
		preview = preview[:toolResultPreviewLen]
	}

	// Count some stats for the model
	lineCount := strings.Count(output, "\n") + 1
	totalChars := len(output)

	return fmt.Sprintf("%s\n\n--- OUTPUT TRUNCATED ---\n"+
		"Full result: %d lines, %d chars\n"+
		"Saved to: %s\n"+
		"Use read_file to see specific sections if needed.",
		preview, lineCount, totalChars, fullPath)
}
