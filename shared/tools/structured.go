package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// structuredOutput creates the structured_output tool function.
// This tool enforces JSON schema-compliant output by validating the agent's
// response against a provided JSON Schema. When an agent needs to produce
// structured data (plans, decisions, configs), this tool acts as a contract.
func structuredOutput() func(ctx context.Context, args json.RawMessage) (string, error) {
	return func(_ context.Context, args json.RawMessage) (string, error) {
		var params struct {
			Schema map[string]any `json:"schema"` // JSON Schema definition
			Data   any            `json:"data"`    // The structured data to validate
		}
		if err := json.Unmarshal(args, &params); err != nil {
			return "", fmt.Errorf("parse structured_output args: %w", err)
		}

		if params.Schema == nil {
			return "", fmt.Errorf("schema is required")
		}
		if params.Data == nil {
			return "", fmt.Errorf("data is required")
		}

		// Validate the data against the schema.
		errors := validateJSON(params.Data, params.Schema, "")
		if len(errors) > 0 {
			return fmt.Sprintf("Validation failed (%d errors):\n%s",
				len(errors), strings.Join(errors, "\n")), fmt.Errorf("schema validation failed")
		}

		// Return the validated data as formatted JSON.
		data, err := json.MarshalIndent(params.Data, "", "  ")
		if err != nil {
			return "", fmt.Errorf("marshal validated data: %w", err)
		}
		return string(data), nil
	}
}

// validateJSON performs basic JSON Schema validation.
// It supports type, required, properties, items, enum, minLength, maxLength,
// minimum, maximum, and pattern constraints.
func validateJSON(data any, schema map[string]any, path string) []string {
	var errors []string

	schemaType, _ := schema["type"].(string)

	switch schemaType {
	case "object":
		obj, ok := data.(map[string]any)
		if !ok {
			return append(errors, fmt.Sprintf("%s: expected object, got %T", pathOrRoot(path), data))
		}

		// Check required fields.
		if req, ok := schema["required"].([]any); ok {
			for _, r := range req {
				name, _ := r.(string)
				if _, exists := obj[name]; !exists {
					errors = append(errors, fmt.Sprintf("%s: missing required field %q", pathOrRoot(path), name))
				}
			}
		}

		// Validate properties.
		if props, ok := schema["properties"].(map[string]any); ok {
			for key, propSchema := range props {
				ps, ok := propSchema.(map[string]any)
				if !ok {
					continue
				}
				val, exists := obj[key]
				if !exists {
					continue // Not required = OK to be absent
				}
				propPath := path + "." + key
				if path == "" {
					propPath = key
				}
				errors = append(errors, validateJSON(val, ps, propPath)...)
			}
		}

	case "array":
		arr, ok := data.([]any)
		if !ok {
			return append(errors, fmt.Sprintf("%s: expected array, got %T", pathOrRoot(path), data))
		}
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range arr {
				itemPath := fmt.Sprintf("%s[%d]", path, i)
				errors = append(errors, validateJSON(item, itemSchema, itemPath)...)
			}
		}
		if minItems, ok := getNum(schema, "minItems"); ok && float64(len(arr)) < minItems {
			errors = append(errors, fmt.Sprintf("%s: array has %d items, minimum %d", pathOrRoot(path), len(arr), int(minItems)))
		}
		if maxItems, ok := getNum(schema, "maxItems"); ok && float64(len(arr)) > maxItems {
			errors = append(errors, fmt.Sprintf("%s: array has %d items, maximum %d", pathOrRoot(path), len(arr), int(maxItems)))
		}

	case "string":
		str, ok := data.(string)
		if !ok {
			return append(errors, fmt.Sprintf("%s: expected string, got %T", pathOrRoot(path), data))
		}
		if minLen, ok := getNum(schema, "minLength"); ok && float64(len(str)) < minLen {
			errors = append(errors, fmt.Sprintf("%s: string length %d < minimum %d", pathOrRoot(path), len(str), int(minLen)))
		}
		if maxLen, ok := getNum(schema, "maxLength"); ok && float64(len(str)) > maxLen {
			errors = append(errors, fmt.Sprintf("%s: string length %d > maximum %d", pathOrRoot(path), len(str), int(maxLen)))
		}
		if enum, ok := schema["enum"].([]any); ok {
			valid := false
			for _, e := range enum {
				if e == str {
					valid = true
					break
				}
			}
			if !valid {
				errors = append(errors, fmt.Sprintf("%s: value %q not in enum %v", pathOrRoot(path), str, enum))
			}
		}

	case "number", "integer":
		num, ok := data.(float64)
		if !ok {
			return append(errors, fmt.Sprintf("%s: expected %s, got %T", pathOrRoot(path), schemaType, data))
		}
		if min, ok := getNum(schema, "minimum"); ok && num < min {
			errors = append(errors, fmt.Sprintf("%s: value %v < minimum %v", pathOrRoot(path), num, min))
		}
		if max, ok := getNum(schema, "maximum"); ok && num > max {
			errors = append(errors, fmt.Sprintf("%s: value %v > maximum %v", pathOrRoot(path), num, max))
		}

	case "boolean":
		if _, ok := data.(bool); !ok {
			errors = append(errors, fmt.Sprintf("%s: expected boolean, got %T", pathOrRoot(path), data))
		}

	case "":
		// No type constraint — any value is valid.
	}

	return errors
}

func pathOrRoot(path string) string {
	if path == "" {
		return "$"
	}
	return path
}

func getNum(schema map[string]any, key string) (float64, bool) {
	v, ok := schema[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}
