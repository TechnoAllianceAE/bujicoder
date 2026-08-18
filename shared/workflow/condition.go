package workflow

import (
	"strings"
)

// EvaluateCondition evaluates a simple condition expression.
// Supported operators:
//   - "{{var}} contains 'text'"         → true if var's value contains text
//   - "{{var}} equals 'text'"           → true if var's value equals text
//   - "{{var}} not_empty"               → true if var's value is non-empty
//   - "{{var}} empty"                    → true if var's value is empty
//
// Returns true if the condition is empty (unconditional step).
func EvaluateCondition(condition string, vars map[string]string) bool {
	if condition == "" {
		return true // No condition = always run
	}

	// First interpolate variables in the condition.
	resolved := Interpolate(condition, vars)

	// Try "X contains 'Y'"
	if idx := strings.Index(resolved, " contains '"); idx >= 0 {
		subject := strings.TrimSpace(resolved[:idx])
		if isUnresolved(subject) {
			return false // an undefined variable contains nothing
		}
		rest := resolved[idx+len(" contains '"):]
		endQuote := strings.IndexByte(rest, '\'')
		if endQuote < 0 {
			return false
		}
		needle := rest[:endQuote]
		return strings.Contains(subject, needle)
	}

	// Try "X equals 'Y'"
	if idx := strings.Index(resolved, " equals '"); idx >= 0 {
		subject := strings.TrimSpace(resolved[:idx])
		rest := resolved[idx+len(" equals '"):]
		endQuote := strings.IndexByte(rest, '\'')
		if endQuote < 0 {
			return false
		}
		expected := rest[:endQuote]
		return strings.TrimSpace(subject) == expected
	}

	// Try "X not_empty"
	if strings.HasSuffix(resolved, " not_empty") {
		subject := strings.TrimSuffix(resolved, " not_empty")
		return !isUnresolved(subject) && strings.TrimSpace(subject) != ""
	}

	// Try "X empty"
	if strings.HasSuffix(resolved, " empty") {
		subject := strings.TrimSuffix(resolved, " empty")
		return isUnresolved(subject) || strings.TrimSpace(subject) == ""
	}

	// Unknown condition format — default to true (run the step).
	return true
}

// isUnresolved reports whether a subject still holds a {{placeholder}}, i.e. the
// variable it referred to was never defined. Treating that as a value made
// "{{missing}} not_empty" true and ran a step that depended on absent output.
func isUnresolved(subject string) bool {
	return strings.Contains(subject, "{{") && strings.Contains(subject, "}}")
}

// Interpolate replaces all {{key}} placeholders in a template with their
// values from the vars map. Unknown keys are left as-is.
func Interpolate(template string, vars map[string]string) string {
	if !strings.Contains(template, "{{") {
		return template
	}

	result := template
	for key, value := range vars {
		placeholder := "{{" + key + "}}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}
