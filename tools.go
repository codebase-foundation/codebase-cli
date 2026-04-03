package main

// ──────────────────────────────────────────────────────────────
//  Legacy helpers — used by permission.go, tasks.go, and other
//  main-package code that hasn't been migrated to internal/ yet.
//
//  Tool definitions, ExecuteTool, and all tool*() functions have
//  been moved to internal/tools/. This file will shrink further
//  as permission.go and tasks.go migrate.
// ──────────────────────────────────────────────────────────────

// ignoreDirs contains directory names to skip during file tree building.
var ignoreDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	"__pycache__": true, "dist": true, ".next": true,
	"build": true, ".cache": true, ".idea": true,
	".vscode": true, "venv": true, ".venv": true,
}

func getString(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func getFloat(args map[string]interface{}, key string) (float64, bool) {
	v, ok := args[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

func getBool(args map[string]interface{}, key string) bool {
	v, ok := args[key]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return b && ok
}
