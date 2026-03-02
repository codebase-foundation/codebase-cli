package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// loadDotEnv loads .env files into os environment variables.
// Only sets vars that aren't already set (real env always wins).
// Searches: .env in cwd, then ~/.codebase/.env.
func loadDotEnv() {
	paths := []string{".env"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".codebase", ".env"))
	}
	for _, p := range paths {
		parseDotEnv(p)
	}
}

func parseDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		// Strip optional "export " prefix
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)

		eq := strings.IndexByte(line, '=')
		if eq < 1 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])

		// Unquote
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') ||
				(val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}

		// Don't override real environment
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
