// Package errutil provides an ErrorOr pattern ported from common/util/error.ts.
// Use success/failure return values instead of throwing exceptions.
package errutil

import "fmt"

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
