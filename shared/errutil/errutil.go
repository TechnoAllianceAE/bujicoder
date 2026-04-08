// Package errutil provides an ErrorOr pattern ported from common/util/error.ts.
// Use success/failure return values instead of throwing exceptions.
package errutil

import "fmt"

// UserError wraps an internal error with a user-friendly message and optional
// action hint. The CLI TUI displays UserMsg instead of the raw error string,
// giving users actionable guidance instead of cryptic Go error text.
type UserError struct {
	Err       error  // Original error (for logging/debugging)
	UserMsg   string // Friendly message shown in the TUI
	Action    string // Suggested action (optional, e.g., "Check your connection and try again")
	Retryable bool   // Whether the operation can be retried
}

func (e *UserError) Error() string { return e.Err.Error() }
func (e *UserError) Unwrap() error { return e.Err }

// ClassifyError wraps a raw error into a UserError with a friendly message
// based on common error patterns from LLM providers and network issues.
func ClassifyError(err error) *UserError {
	if err == nil {
		return nil
	}
	// Already classified
	var ue *UserError
	if As(err, &ue) {
		return ue
	}

	msg := err.Error()

	switch {
	// Network / timeout
	case contains(msg, "context deadline exceeded"),
		contains(msg, "Client.Timeout"):
		return &UserError{Err: err, UserMsg: "Request timed out", Action: "The model took too long to respond. Try again or switch to a faster model.", Retryable: true}

	case contains(msg, "context canceled"):
		return &UserError{Err: err, UserMsg: "Request cancelled", Action: "The request was interrupted.", Retryable: true}

	case contains(msg, "connection refused"),
		contains(msg, "no such host"),
		contains(msg, "dial tcp"):
		return &UserError{Err: err, UserMsg: "Cannot reach the server", Action: "Check your network connection and server URL.", Retryable: true}

	// Auth
	case contains(msg, "401"), contains(msg, "unauthorized"), contains(msg, "Unauthorized"):
		return &UserError{Err: err, UserMsg: "Authentication failed", Action: "Your API key may be invalid or expired. Check your configuration."}

	// Rate limiting
	case contains(msg, "429"), contains(msg, "rate limit"), contains(msg, "Rate limit"):
		return &UserError{Err: err, UserMsg: "Rate limit exceeded", Action: "Too many requests. Wait a moment and try again.", Retryable: true}

	case contains(msg, "session usage limit"), contains(msg, "Key limit exceeded"):
		return &UserError{Err: err, UserMsg: "Usage limit reached", Action: "You've hit the provider's usage limit. Wait or check your plan."}

	// Provider errors
	case contains(msg, "insufficient credits"), contains(msg, "billing"):
		return &UserError{Err: err, UserMsg: "Insufficient credits", Action: "Your account has no remaining credits. Top up or switch providers."}

	case contains(msg, "model") && contains(msg, "not found"):
		return &UserError{Err: err, UserMsg: "Model not available", Action: "The requested model doesn't exist or isn't configured. Try a different model."}

	case contains(msg, "disabled"), contains(msg, "exceeding the"):
		return &UserError{Err: err, UserMsg: "Model not allowed", Action: "This model is restricted. Try a different model."}

	case contains(msg, "all models failed"):
		return &UserError{Err: err, UserMsg: "All models unavailable", Action: "The primary model and all fallbacks failed. Try again later.", Retryable: true}

	case contains(msg, "provider error"):
		return &UserError{Err: err, UserMsg: "Model provider error", Action: "The upstream model provider returned an error. Try again.", Retryable: true}

	// Agent runtime
	case contains(msg, "agent") && contains(msg, "not found"):
		return &UserError{Err: err, UserMsg: "Agent configuration error", Action: "The requested agent is not available. Check your agent definitions."}

	case contains(msg, "route model"):
		return &UserError{Err: err, UserMsg: "Model routing failed", Action: "Could not route to the configured model. Check model_config.yaml."}

	default:
		return &UserError{Err: err, UserMsg: "Something went wrong", Action: "An unexpected error occurred. Try again or check the logs.", Retryable: true}
	}
}

// As is a convenience wrapper around errors.As for UserError.
func As(err error, target interface{}) bool {
	return fmt.Errorf("%w", err) != nil && asImpl(err, target)
}

func asImpl(err error, target interface{}) bool {
	if target == nil {
		return false
	}
	// Use errors.As from stdlib
	type unwrapper interface{ Unwrap() error }
	if ue, ok := target.(**UserError); ok {
		if e, ok2 := err.(*UserError); ok2 {
			*ue = e
			return true
		}
		if u, ok2 := err.(unwrapper); ok2 {
			return asImpl(u.Unwrap(), target)
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Result represents either a success value or an error.
type Result[T any] struct {
	value T
	err   error
	ok    bool
}

// Success creates a successful result.
func Success[T any](value T) Result[T] {
	return Result[T]{value: value, ok: true}
}

// Failure creates a failed result.
func Failure[T any](err error) Result[T] {
	return Result[T]{err: err, ok: false}
}

// Failuref creates a failed result with a formatted error message.
func Failuref[T any](format string, args ...any) Result[T] {
	return Result[T]{err: fmt.Errorf(format, args...), ok: false}
}

// IsOk returns true if the result is successful.
func (r Result[T]) IsOk() bool {
	return r.ok
}

// IsErr returns true if the result is a failure.
func (r Result[T]) IsErr() bool {
	return !r.ok
}

// Value returns the success value. Panics if the result is a failure.
func (r Result[T]) Value() T {
	if !r.ok {
		panic(fmt.Sprintf("called Value() on error result: %v", r.err))
	}
	return r.value
}

// Err returns the error. Returns nil if the result is successful.
func (r Result[T]) Err() error {
	return r.err
}

// Unwrap returns the value and error separately, similar to Go convention.
func (r Result[T]) Unwrap() (T, error) {
	return r.value, r.err
}

// Map transforms the success value using the given function.
func Map[T, U any](r Result[T], fn func(T) U) Result[U] {
	if r.ok {
		return Success(fn(r.value))
	}
	return Failure[U](r.err)
}

// FlatMap transforms the success value using a function that returns a Result.
func FlatMap[T, U any](r Result[T], fn func(T) Result[U]) Result[U] {
	if r.ok {
		return fn(r.value)
	}
	return Failure[U](r.err)
}
