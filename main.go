package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────────
//  Configuration
// ──────────────────────────────────────────────────────────────

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
	WorkDir string

	// Glue sidecar (optional — falls back to main OPENAI_* vars)
	GlueAPIKey     string
	GlueBaseURL    string
	GlueFastModel  string
	GlueSmartModel string
}

func loadConfig() (*Config, error) {
	// CLI flags
	model := flag.String("model", "", "LLM model name (default: gpt-4o)")
	dir := flag.String("dir", "", "Working directory (default: current dir)")
	baseURL := flag.String("base-url", "", "OpenAI-compatible API base URL")
	flag.Parse()

	cfg := &Config{}

	// API key (required)
	cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable is required")
	}

	// Base URL
	cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	if *baseURL != "" {
		cfg.BaseURL = *baseURL
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}

	// Model
	cfg.Model = os.Getenv("OPENAI_MODEL")
	if *model != "" {
		cfg.Model = *model
	}
	if cfg.Model == "" {
		cfg.Model = "gpt-4o"
	}

	// Working directory
	if *dir != "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			return nil, fmt.Errorf("invalid directory: %w", err)
		}
		cfg.WorkDir = abs
	} else {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine working directory: %w", err)
		}
		cfg.WorkDir = wd
	}

	// Verify work dir exists
	info, err := os.Stat(cfg.WorkDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("working directory does not exist: %s", cfg.WorkDir)
	}

	// Glue sidecar config (all optional)
	cfg.GlueAPIKey = os.Getenv("GLUE_API_KEY")
	cfg.GlueBaseURL = os.Getenv("GLUE_BASE_URL")
	cfg.GlueFastModel = os.Getenv("GLUE_FAST_MODEL")
	cfg.GlueSmartModel = os.Getenv("GLUE_SMART_MODEL")

	return cfg, nil
}

// ──────────────────────────────────────────────────────────────
//  Entry point
// ──────────────────────────────────────────────────────────────

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Clean up stale sessions in the background
	go CleanStaleSessions()

	p := tea.NewProgram(
		newAppModel(cfg),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
