package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateArgs_RequiredFields(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"path": {"type": "string"},
			"content": {"type": "string"}
		},
		"required": ["path", "content"]
	}`)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
		errMsg  string
	}{
		{
			name:    "all required present",
			args:    map[string]any{"path": "foo.go", "content": "bar"},
			wantErr: false,
		},
		{
			name:    "missing one required",
			args:    map[string]any{"path": "foo.go"},
			wantErr: true,
			errMsg:  "missing required field \"content\"",
		},
		{
			name:    "missing all required",
			args:    map[string]any{},
			wantErr: true,
			errMsg:  "missing required field",
		},
		{
			name:    "extra fields allowed",
			args:    map[string]any{"path": "foo.go", "content": "bar", "extra": true},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(schema, tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error %q doesn't contain %q", err, tt.errMsg)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgs_TypeChecking(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name":    {"type": "string"},
			"count":   {"type": "number"},
			"enabled": {"type": "boolean"},
			"items":   {"type": "array"},
			"config":  {"type": "object"}
		}
	}`)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name:    "correct types",
			args:    map[string]any{"name": "foo", "count": 5.0, "enabled": true, "items": []any{"a"}, "config": map[string]any{}},
			wantErr: false,
		},
		{
			name:    "string where number expected",
			args:    map[string]any{"count": "five"},
			wantErr: true,
		},
		{
			name:    "number where string expected",
			args:    map[string]any{"name": 42.0},
			wantErr: true,
		},
		{
			name:    "string where boolean expected",
			args:    map[string]any{"enabled": "true"},
			wantErr: true,
		},
		{
			name:    "nil values pass",
			args:    map[string]any{"name": nil},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(schema, tt.args)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgs_NestedObject(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"edit": {
				"type": "object",
				"properties": {
					"path":     {"type": "string"},
					"old_text": {"type": "string"}
				},
				"required": ["path", "old_text"]
			}
		}
	}`)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name: "valid nested",
			args: map[string]any{"edit": map[string]any{"path": "a.go", "old_text": "foo"}},
		},
		{
			name:    "missing nested required",
			args:    map[string]any{"edit": map[string]any{"path": "a.go"}},
			wantErr: true,
		},
		{
			name:    "wrong nested type",
			args:    map[string]any{"edit": map[string]any{"path": 42.0, "old_text": "foo"}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(schema, tt.args)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgs_ArrayItems(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"files": {
				"type": "array",
				"items": {"type": "string"}
			}
		}
	}`)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{
			name: "valid string array",
			args: map[string]any{"files": []any{"a.go", "b.go"}},
		},
		{
			name: "empty array",
			args: map[string]any{"files": []any{}},
		},
		{
			name:    "number in string array",
			args:    map[string]any{"files": []any{"a.go", 42.0}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(schema, tt.args)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgs_Enum(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"mode": {
				"type": "string",
				"enum": ["fast", "slow", "auto"]
			}
		}
	}`)

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{name: "valid enum", args: map[string]any{"mode": "fast"}},
		{name: "invalid enum", args: map[string]any{"mode": "turbo"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(schema, tt.args)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateArgs_EmptySchema(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "properties": {}, "required": []}`)
	err := ValidateArgs(schema, map[string]any{"anything": "goes"})
	if err != nil {
		t.Fatalf("empty schema should accept anything: %v", err)
	}
}
