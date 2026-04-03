package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────────
//  Lightweight JSON Schema validator
//
//  Validates tool arguments against their declared JSON Schema.
//  Covers the subset of JSON Schema that tool arguments actually use:
//    - type checking (string, number, boolean, array, object)
//    - required fields
//    - nested object properties
//    - array item type checking
//    - enum validation
//
//  No external dependencies. Not a full JSON Schema implementation —
//  just enough for tool arg validation.
// ──────────────────────────────────────────────────────────────

// ValidateArgs checks that args conform to the given JSON Schema.
// Returns nil if valid, or a descriptive error listing all violations.
func ValidateArgs(schema json.RawMessage, args map[string]any) error {
	var s schemaNode
	if err := json.Unmarshal(schema, &s); err != nil {
		return fmt.Errorf("invalid schema: %w", err)
	}
	var errs []string
	validateObject(&s, args, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	return &ValidationError{Errors: errs}
}

// ValidationError holds all schema violations found during validation.
type ValidationError struct {
	Errors []string
}

func (e *ValidationError) Error() string {
	return "validation failed: " + strings.Join(e.Errors, "; ")
}

// ──────────────────────────────────────────────────────────────
//  Internal schema representation
// ──────────────────────────────────────────────────────────────

type schemaNode struct {
	Type        string                `json:"type"`
	Properties  map[string]schemaNode `json:"properties"`
	Required    []string              `json:"required"`
	Items       *schemaNode           `json:"items"`
	Enum        []any                 `json:"enum"`
	Description string                `json:"description"`
}

// ──────────────────────────────────────────────────────────────
//  Validation logic
// ──────────────────────────────────────────────────────────────

func validateObject(s *schemaNode, args map[string]any, path string, errs *[]string) {
	// Check required fields
	for _, req := range s.Required {
		if _, ok := args[req]; !ok {
			*errs = append(*errs, fmt.Sprintf("%s: missing required field %q", fieldPath(path, req), req))
		}
	}

	// Validate each provided field against its property schema
	for key, val := range args {
		propSchema, known := s.Properties[key]
		if !known {
			// Unknown fields are allowed — LLMs sometimes add extra fields,
			// and being strict here would cause more harm than good.
			continue
		}
		validateValue(&propSchema, val, fieldPath(path, key), errs)
	}
}

func validateValue(s *schemaNode, val any, path string, errs *[]string) {
	// Null/nil check
	if val == nil {
		// null is generally acceptable unless we want to enforce non-null
		return
	}

	// Type check
	if s.Type != "" && !checkType(s.Type, val) {
		*errs = append(*errs, fmt.Sprintf("%s: expected type %q, got %T", path, s.Type, val))
		return // don't validate further on type mismatch
	}

	// Enum check
	if len(s.Enum) > 0 && !checkEnum(s.Enum, val) {
		*errs = append(*errs, fmt.Sprintf("%s: value %v not in enum %v", path, val, s.Enum))
	}

	// Recurse into objects
	if s.Type == "object" && s.Properties != nil {
		if obj, ok := val.(map[string]any); ok {
			validateObject(s, obj, path, errs)
		}
	}

	// Recurse into arrays
	if s.Type == "array" && s.Items != nil {
		if arr, ok := val.([]any); ok {
			for i, item := range arr {
				validateValue(s.Items, item, fmt.Sprintf("%s[%d]", path, i), errs)
			}
		}
	}
}

// checkType returns true if val matches the JSON Schema type string.
// JSON numbers are always float64 in Go's json.Unmarshal.
func checkType(typ string, val any) bool {
	switch typ {
	case "string":
		_, ok := val.(string)
		return ok
	case "number", "integer":
		_, ok := val.(float64)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	default:
		return true // unknown type = permissive
	}
}

func checkEnum(enum []any, val any) bool {
	for _, e := range enum {
		if fmt.Sprintf("%v", e) == fmt.Sprintf("%v", val) {
			return true
		}
	}
	return false
}

func fieldPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}
