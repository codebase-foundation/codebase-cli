package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  File History — auto-snapshot before every edit
//
//  Circular buffer of file snapshots taken before each write/edit.
//  Powers /undo beyond git — can revert to any point in the session.
//  Stored in memory for speed (not disk).
//
//  CC tracks this in fileHistory.ts with a 100-snapshot cap.
//  Ours is simpler: same cap, same hash-based dedup, but no
//  disk persistence (session-only, which is fine for undo).
// ──────────────────────────────────────────────────────────────

const maxSnapshots = 100

// FileSnapshot captures a file's state before modification.
type FileSnapshot struct {
	Path      string    // absolute path
	RelPath   string    // relative to workDir
	Content   []byte    // file contents at snapshot time
	Hash      string    // sha256 of content
	Timestamp time.Time // when snapshot was taken
	Turn      int       // which agent turn triggered this
}

// FileHistory tracks file snapshots for undo support.
type FileHistory struct {
	mu        sync.Mutex
	snapshots []FileSnapshot
	seqNo     int // monotonic counter for ordering
}

// NewFileHistory creates an empty file history.
func NewFileHistory() *FileHistory {
	return &FileHistory{}
}

// Snapshot saves the current state of a file before it gets modified.
// Call this BEFORE writing/editing. Safe to call for nonexistent files
// (records empty snapshot so undo = delete).
func (h *FileHistory) Snapshot(absPath, relPath string, turn int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	content, err := os.ReadFile(absPath)
	if err != nil {
		// File doesn't exist yet — record empty snapshot
		// so undo means "delete the file"
		content = nil
	}

	hash := hashContent(content)

	// Dedup: if the last snapshot of this file has the same hash, skip
	for i := len(h.snapshots) - 1; i >= 0; i-- {
		if h.snapshots[i].Path == absPath {
			if h.snapshots[i].Hash == hash {
				return // no change since last snapshot
			}
			break // found a different snapshot, proceed
		}
	}

	snap := FileSnapshot{
		Path:      absPath,
		RelPath:   relPath,
		Content:   content,
		Hash:      hash,
		Timestamp: time.Now(),
		Turn:      turn,
	}

	h.snapshots = append(h.snapshots, snap)
	h.seqNo++

	// Enforce cap — evict oldest
	if len(h.snapshots) > maxSnapshots {
		h.snapshots = h.snapshots[len(h.snapshots)-maxSnapshots:]
	}
}

// Undo reverts a file to its most recent snapshot.
// Returns the snapshot that was restored, or nil if no snapshot exists.
func (h *FileHistory) Undo(absPath string) *FileSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Find most recent snapshot for this file
	for i := len(h.snapshots) - 1; i >= 0; i-- {
		snap := h.snapshots[i]
		if snap.Path == absPath {
			// Restore
			if snap.Content == nil {
				// File didn't exist before — delete it
				os.Remove(absPath)
			} else {
				os.WriteFile(absPath, snap.Content, 0644)
			}

			// Remove this snapshot (consumed)
			h.snapshots = append(h.snapshots[:i], h.snapshots[i+1:]...)
			return &snap
		}
	}
	return nil
}

// UndoByRelPath finds and reverts by relative path.
func (h *FileHistory) UndoByRelPath(relPath string) *FileSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	for i := len(h.snapshots) - 1; i >= 0; i-- {
		snap := h.snapshots[i]
		if snap.RelPath == relPath {
			if snap.Content == nil {
				os.Remove(snap.Path)
			} else {
				os.WriteFile(snap.Path, snap.Content, 0644)
			}
			h.snapshots = append(h.snapshots[:i], h.snapshots[i+1:]...)
			return &snap
		}
	}
	return nil
}

// Recent returns the N most recent snapshots (newest first).
func (h *FileHistory) Recent(n int) []FileSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	if n > len(h.snapshots) {
		n = len(h.snapshots)
	}

	result := make([]FileSnapshot, n)
	for i := 0; i < n; i++ {
		result[i] = h.snapshots[len(h.snapshots)-1-i]
	}
	return result
}

// Count returns the number of stored snapshots.
func (h *FileHistory) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.snapshots)
}

func hashContent(data []byte) string {
	if data == nil {
		return "nil"
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8]) // 16 hex chars is enough for dedup
}
