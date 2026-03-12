package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStructuredOutput_ValidObject(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"name": {"type": "string"},
				"age": {"type": "number"}
			},
			"required": ["name"]
		},
		"data": {
			"name": "Alice",
			"age": 30
		}
	}`

	result, err := fn(nil, json.RawMessage(args))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(result, "Alice") {
		t.Errorf("expected Alice in result, got: %s", result)
	}
}

func TestStructuredOutput_MissingRequired(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"name": {"type": "string"},
				"age": {"type": "number"}
			},
			"required": ["name", "age"]
		},
		"data": {
			"name": "Bob"
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for missing required field")
	}
}

func TestStructuredOutput_WrongType(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"count": {"type": "number"}
			}
		},
		"data": {
			"count": "not a number"
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for wrong type")
	}
}

func TestStructuredOutput_ValidArray(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"id": {"type": "number"},
					"task": {"type": "string"}
				},
				"required": ["id", "task"]
			}
		},
		"data": [
			{"id": 1, "task": "Buy groceries"},
			{"id": 2, "task": "Walk the dog"}
		]
	}`

	result, err := fn(nil, json.RawMessage(args))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(result, "Buy groceries") {
		t.Errorf("expected task in result, got: %s", result)
	}
}

func TestStructuredOutput_EnumValidation(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"status": {
					"type": "string",
					"enum": ["active", "inactive", "pending"]
				}
			}
		},
		"data": {
			"status": "unknown"
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for enum mismatch")
	}
}

func TestStructuredOutput_StringConstraints(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"code": {
					"type": "string",
					"minLength": 3,
					"maxLength": 10
				}
			}
		},
		"data": {
			"code": "AB"
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for minLength")
	}
}

func TestStructuredOutput_NumberConstraints(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"score": {
					"type": "number",
					"minimum": 0,
					"maximum": 100
				}
			}
		},
		"data": {
			"score": 150
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for maximum")
	}
}

func TestStructuredOutput_NoSchema(t *testing.T) {
	fn := structuredOutput()
	args := `{"data": {"name": "test"}}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected error when schema is missing")
	}
}

func TestStructuredOutput_NoData(t *testing.T) {
	fn := structuredOutput()
	args := `{"schema": {"type": "object"}}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected error when data is missing")
	}
}

func TestStructuredOutput_NestedObject(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"user": {
					"type": "object",
					"properties": {
						"name": {"type": "string"},
						"email": {"type": "string"}
					},
					"required": ["name"]
				}
			},
			"required": ["user"]
		},
		"data": {
			"user": {
				"name": "Charlie",
				"email": "charlie@example.com"
			}
		}
	}`

	result, err := fn(nil, json.RawMessage(args))
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if !strings.Contains(result, "Charlie") {
		t.Errorf("expected nested data in result, got: %s", result)
	}
}

func TestStructuredOutput_BooleanType(t *testing.T) {
	fn := structuredOutput()
	args := `{
		"schema": {
			"type": "object",
			"properties": {
				"active": {"type": "boolean"}
			}
		},
		"data": {
			"active": "yes"
		}
	}`

	_, err := fn(nil, json.RawMessage(args))
	if err == nil {
		t.Error("expected validation error for boolean type mismatch")
	}
}
