package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ──────────────────────────────────────────────────────────────
//  Diagnostics engine — run language checkers after file edits
// ──────────────────────────────────────────────────────────────

// Diagnostic represents a single error/warning from a checker.
type Diagnostic struct {
	File     string
	Line     int
	Column   int
	Severity string // "error", "warning"
	Message  string
}

// LanguageChecker defines how to check files for a language.
type LanguageChecker struct {
	Name       string
	Extensions []string
	Command    func(workDir string) (string, []string)
	Parse      func(output string) []Diagnostic
	Detect     func(workDir string) bool
}

// DiagnosticsEngine manages language detection and checker execution.
type DiagnosticsEngine struct {
	workDir  string
	checkers []LanguageChecker
	Enabled  bool
}

// defaultCheckers defines supported language checkers.
var defaultCheckers = []LanguageChecker{
	{
		Name:       "Go",
		Extensions: []string{".go"},
		Detect: func(workDir string) bool {
			_, err := os.Stat(filepath.Join(workDir, "go.mod"))
			return err == nil
		},
		Command: func(workDir string) (string, []string) {
			return "go", []string{"vet", "./..."}
		},
		Parse: parseGoVetOutput,
	},
	{
		Name:       "Go Build",
		Extensions: []string{".go"},
		Detect: func(workDir string) bool {
			_, err := os.Stat(filepath.Join(workDir, "go.mod"))
			return err == nil
		},
		Command: func(workDir string) (string, []string) {
			return "go", []string{"build", "./..."}
		},
		Parse: parseGoVetOutput, // same format: file:line:col: message
	},
	{
		Name:       "TypeScript",
		Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		Detect: func(workDir string) bool {
			if _, err := os.Stat(filepath.Join(workDir, "tsconfig.json")); err == nil {
				return true
			}
			return false
		},
		Command: func(workDir string) (string, []string) {
			if _, err := os.Stat(filepath.Join(workDir, "node_modules", ".bin", "tsc")); err == nil {
				return "npx", []string{"tsc", "--noEmit", "--pretty", "false"}
			}
			if _, err := exec.LookPath("tsc"); err == nil {
				return "tsc", []string{"--noEmit", "--pretty", "false"}
			}
			return "", nil
		},
		Parse: parseTscOutput,
	},
	{
		Name:       "ESLint",
		Extensions: []string{".ts", ".tsx", ".js", ".jsx"},
		Detect: func(workDir string) bool {
			for _, cfg := range []string{".eslintrc.js", ".eslintrc.json", ".eslintrc.yml", "eslint.config.js", "eslint.config.mjs"} {
				if _, err := os.Stat(filepath.Join(workDir, cfg)); err == nil {
					return true
				}
			}
			return false
		},
		Command: func(workDir string) (string, []string) {
			if _, err := os.Stat(filepath.Join(workDir, "node_modules", ".bin", "eslint")); err == nil {
				return "npx", []string{"eslint", "--format", "unix", "--quiet", "."}
			}
			if _, err := exec.LookPath("eslint"); err == nil {
				return "eslint", []string{"--format", "unix", "--quiet", "."}
			}
			return "", nil
		},
		Parse: parseEslintUnixOutput,
	},
	{
		Name:       "Python",
		Extensions: []string{".py"},
		Detect: func(workDir string) bool {
			_, err := exec.LookPath("pyright")
			if err == nil {
				return true
			}
			_, err = exec.LookPath("mypy")
			return err == nil
		},
		Command: func(workDir string) (string, []string) {
			if _, err := exec.LookPath("pyright"); err == nil {
				return "pyright", []string{"--outputjson"}
			}
			if _, err := exec.LookPath("mypy"); err == nil {
				return "mypy", []string{"--no-color-output", "--no-error-summary"}
			}
			return "", nil
		},
		Parse: parsePythonOutput,
	},
	{
		Name:       "Rust",
		Extensions: []string{".rs"},
		Detect: func(workDir string) bool {
			_, err := os.Stat(filepath.Join(workDir, "Cargo.toml"))
			return err == nil
		},
		Command: func(workDir string) (string, []string) {
			return "cargo", []string{"check", "--message-format=short", "2>&1"}
		},
		Parse: parseCargoOutput,
	},
}

// NewDiagnosticsEngine creates and initializes a diagnostics engine.
func NewDiagnosticsEngine(workDir string) *DiagnosticsEngine {
	de := &DiagnosticsEngine{
		workDir: workDir,
		Enabled: true,
	}

	// Auto-detect available checkers
	for _, checker := range defaultCheckers {
		if checker.Detect(workDir) {
			de.checkers = append(de.checkers, checker)
		}
	}

	return de
}

// CheckFiles runs relevant checkers for the given files.
// Returns diagnostics, or nil on timeout/no checker.
func (de *DiagnosticsEngine) CheckFiles(files []string) []Diagnostic {
	if !de.Enabled || len(de.checkers) == 0 {
		return nil
	}

	// Find matching checker by file extension
	var checker *LanguageChecker
	for _, f := range files {
		ext := filepath.Ext(f)
		for i := range de.checkers {
			for _, cExt := range de.checkers[i].Extensions {
				if ext == cExt {
					checker = &de.checkers[i]
					break
				}
			}
			if checker != nil {
				break
			}
		}
		if checker != nil {
			break
		}
	}

	if checker == nil {
		return nil
	}

	cmdName, cmdArgs := checker.Command(de.workDir)
	if cmdName == "" {
		return nil
	}

	cmd := exec.Command(cmdName, cmdArgs...)
	cmd.Dir = de.workDir
	cmd.Env = append(os.Environ(), "NO_COLOR=1")

	done := make(chan []byte, 1)
	go func() {
		out, _ := cmd.CombinedOutput()
		done <- out
	}()

	select {
	case out := <-done:
		return checker.Parse(string(out))
	case <-time.After(15 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil
	}
}

// DetectedCheckers returns the names of auto-detected checkers.
func (de *DiagnosticsEngine) DetectedCheckers() []string {
	names := make([]string, len(de.checkers))
	for i, c := range de.checkers {
		names[i] = c.Name
	}
	return names
}

// ── Parsers ──────────────────────────────────────────────────

var goVetRegex = regexp.MustCompile(`^(.+\.go):(\d+):(\d+):\s*(.+)$`)

// parseGoVetOutput parses "file.go:line:col: message" format.
func parseGoVetOutput(output string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := goVetRegex.FindStringSubmatch(line); m != nil {
			lineNo, _ := strconv.Atoi(m[2])
			colNo, _ := strconv.Atoi(m[3])
			diags = append(diags, Diagnostic{
				File:     m[1],
				Line:     lineNo,
				Column:   colNo,
				Severity: "error",
				Message:  m[4],
			})
		}
	}
	return diags
}

var tscRegex = regexp.MustCompile(`^(.+)\((\d+),(\d+)\):\s*(error|warning)\s+\w+:\s*(.+)$`)

// parseTscOutput parses "file(line,col): error TSxxxx: message" format.
func parseTscOutput(output string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := tscRegex.FindStringSubmatch(line); m != nil {
			lineNo, _ := strconv.Atoi(m[2])
			colNo, _ := strconv.Atoi(m[3])
			diags = append(diags, Diagnostic{
				File:     m[1],
				Line:     lineNo,
				Column:   colNo,
				Severity: m[4],
				Message:  m[5],
			})
		}
	}
	return diags
}

// parsePyrightOutput parses pyright --outputjson format.
func parsePyrightOutput(output string) []Diagnostic {
	var result struct {
		GeneralDiagnostics []struct {
			File     string `json:"file"`
			Range    struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
			Severity string `json:"severity"`
			Message  string `json:"message"`
		} `json:"generalDiagnostics"`
	}

	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil
	}

	var diags []Diagnostic
	for _, d := range result.GeneralDiagnostics {
		severity := "warning"
		if d.Severity == "error" {
			severity = "error"
		}
		diags = append(diags, Diagnostic{
			File:     d.File,
			Line:     d.Range.Start.Line + 1, // pyright uses 0-based lines
			Column:   d.Range.Start.Character + 1,
			Severity: severity,
			Message:  d.Message,
		})
	}
	return diags
}

// eslint unix format: "file.ts:line:col: message [severity/rule]"
var eslintUnixRegex = regexp.MustCompile(`^(.+):(\d+):(\d+):\s*(.+?)(?:\s*\[(\w+)/\w+\])?$`)

func parseEslintUnixOutput(output string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := eslintUnixRegex.FindStringSubmatch(line); m != nil {
			lineNo, _ := strconv.Atoi(m[2])
			colNo, _ := strconv.Atoi(m[3])
			severity := "warning"
			if m[5] == "Error" || m[5] == "error" {
				severity = "error"
			}
			diags = append(diags, Diagnostic{
				File:     m[1],
				Line:     lineNo,
				Column:   colNo,
				Severity: severity,
				Message:  m[4],
			})
		}
	}
	return diags
}

// parsePythonOutput handles both pyright JSON and mypy text output.
func parsePythonOutput(output string) []Diagnostic {
	// Try pyright JSON first
	if diags := parsePyrightOutput(output); len(diags) > 0 {
		return diags
	}
	// Fall back to mypy format: "file.py:line: error: message"
	return parseMypyOutput(output)
}

var mypyRegex = regexp.MustCompile(`^(.+\.py):(\d+):\s*(error|warning|note):\s*(.+)$`)

func parseMypyOutput(output string) []Diagnostic {
	var diags []Diagnostic
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if m := mypyRegex.FindStringSubmatch(line); m != nil {
			lineNo, _ := strconv.Atoi(m[2])
			severity := m[3]
			if severity == "note" {
				continue // skip notes
			}
			diags = append(diags, Diagnostic{
				File:     m[1],
				Line:     lineNo,
				Column:   0,
				Severity: severity,
				Message:  m[4],
			})
		}
	}
	return diags
}

// cargo check --message-format=short: "error[E0308]: file.rs:10:5: message"
var cargoRegex = regexp.MustCompile(`^(error|warning)(?:\[E\d+\])?:\s*(.+)$`)
var cargoLocRegex = regexp.MustCompile(`^\s*--> (.+):(\d+):(\d+)$`)

func parseCargoOutput(output string) []Diagnostic {
	var diags []Diagnostic
	var currentSeverity, currentMessage string

	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)

		// Match error/warning header
		if m := cargoRegex.FindStringSubmatch(line); m != nil {
			currentSeverity = m[1]
			currentMessage = m[2]
			continue
		}

		// Match location line
		if m := cargoLocRegex.FindStringSubmatch(line); m != nil && currentMessage != "" {
			lineNo, _ := strconv.Atoi(m[2])
			colNo, _ := strconv.Atoi(m[3])
			diags = append(diags, Diagnostic{
				File:     m[1],
				Line:     lineNo,
				Column:   colNo,
				Severity: currentSeverity,
				Message:  currentMessage,
			})
			currentMessage = ""
		}
	}
	return diags
}

// ── Formatting ───────────────────────────────────────────────

// formatDiagnosticsMessage creates a concise message for the agent.
func formatDiagnosticsMessage(diags []Diagnostic) string {
	var sb strings.Builder
	sb.WriteString("## Language Diagnostics\n\n")
	sb.WriteString("The following issues were detected after your recent edits:\n\n")

	// Group by file
	byFile := make(map[string][]Diagnostic)
	order := []string{}
	for _, d := range diags {
		if _, seen := byFile[d.File]; !seen {
			order = append(order, d.File)
		}
		byFile[d.File] = append(byFile[d.File], d)
	}

	for _, file := range order {
		fileDiags := byFile[file]
		sb.WriteString(fmt.Sprintf("### %s\n", file))
		for _, d := range fileDiags {
			sb.WriteString(fmt.Sprintf("- Line %d:%d [%s]: %s\n",
				d.Line, d.Column, d.Severity, d.Message))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("Please fix these issues before continuing.")
	return sb.String()
}
