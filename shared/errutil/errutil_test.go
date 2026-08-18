package errutil

import (
	"errors"
	"fmt"
	"testing"
)

func TestResultSuccess(t *testing.T) {
	r := Success(42)
	if !r.IsOk() || r.IsErr() {
		t.Fatalf("Success is not ok: ok=%v err=%v", r.IsOk(), r.IsErr())
	}
	if got := r.Value(); got != 42 {
		t.Fatalf("Value() = %d, want 42", got)
	}
	if err := r.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	v, err := r.Unwrap()
	if v != 42 || err != nil {
		t.Fatalf("Unwrap() = (%d, %v), want (42, nil)", v, err)
	}
}

// A failed Result must never hand back a nil error next to a zero value:
// callers written as `v, err := r.Unwrap(); if err != nil {...}` would silently
// accept the zero value as a successful result.
func TestFailedResultNeverLooksLikeSuccess(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name    string
		result  Result[int]
		wantErr error
	}{
		{name: "Failure with an error", result: Failure[int](sentinel), wantErr: sentinel},
		{name: "Failure with nil error", result: Failure[int](nil), wantErr: ErrUnspecified},
		{name: "zero value Result", result: Result[int]{}, wantErr: ErrUnspecified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.result.IsOk() {
				t.Fatal("failed result reports IsOk")
			}
			if err := tc.result.Err(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Err() = %v, want %v", err, tc.wantErr)
			}
			v, err := tc.result.Unwrap()
			if err == nil {
				t.Fatal("Unwrap() returned a nil error for a failed result")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Unwrap() error = %v, want %v", err, tc.wantErr)
			}
			if v != 0 {
				t.Fatalf("Unwrap() value = %d, want the zero value", v)
			}
		})
	}
}

func TestFailurefWrapsErrors(t *testing.T) {
	sentinel := errors.New("root cause")
	r := Failuref[string]("load config: %w", sentinel)

	if r.IsOk() {
		t.Fatal("Failuref produced an ok result")
	}
	if !errors.Is(r.Err(), sentinel) {
		t.Fatalf("Failuref lost the wrapped error: %v", r.Err())
	}
}

func TestValuePanicsOnFailure(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Value() on a failed result must panic instead of returning a zero value")
		}
	}()
	_ = Failure[int](errors.New("nope")).Value()
}

func TestMapAndFlatMapPreserveFailure(t *testing.T) {
	sentinel := errors.New("root")

	mapped := Map(Failure[int](sentinel), func(i int) string { return fmt.Sprint(i) })
	if mapped.IsOk() {
		t.Fatal("Map turned a failure into a success")
	}
	if !errors.Is(mapped.Err(), sentinel) {
		t.Fatalf("Map lost the error: %v", mapped.Err())
	}

	flat := FlatMap(Failure[int](sentinel), func(i int) Result[string] {
		t.Fatal("FlatMap must not call fn on a failure")
		return Success("unreachable")
	})
	if !errors.Is(flat.Err(), sentinel) {
		t.Fatalf("FlatMap lost the error: %v", flat.Err())
	}

	okMapped := Map(Success(7), func(i int) string { return fmt.Sprint(i * 2) })
	if !okMapped.IsOk() || okMapped.Value() != "14" {
		t.Fatalf("Map on success = %+v", okMapped)
	}
	okFlat := FlatMap(Success(7), func(i int) Result[string] { return Success(fmt.Sprint(i)) })
	if !okFlat.IsOk() || okFlat.Value() != "7" {
		t.Fatalf("FlatMap on success = %+v", okFlat)
	}
}

func TestUserErrorErrorDoesNotPanicWithoutCause(t *testing.T) {
	tests := []struct {
		name string
		ue   *UserError
		want string
	}{
		{name: "wrapped cause", ue: &UserError{Err: errors.New("inner"), UserMsg: "friendly"}, want: "inner"},
		{name: "no cause falls back to UserMsg", ue: &UserError{UserMsg: "friendly"}, want: "friendly"},
		{name: "no cause and no message", ue: &UserError{}, want: "unspecified error"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ue.Error(); got != tc.want {
				t.Fatalf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// As must walk the entire error chain the way errors.As does, including
// errors.Join, and must not report false just because the target is not a
// **UserError.
func TestAsWalksTheWholeChain(t *testing.T) {
	target := &UserError{Err: errors.New("inner"), UserMsg: "friendly"}

	t.Run("direct", func(t *testing.T) {
		var got *UserError
		if !As(error(target), &got) || got != target {
			t.Fatalf("As failed on a direct match: got=%v", got)
		}
	})

	t.Run("single wrap", func(t *testing.T) {
		var got *UserError
		if !As(fmt.Errorf("ctx: %w", target), &got) || got != target {
			t.Fatalf("As failed through fmt.Errorf: got=%v", got)
		}
	})

	t.Run("joined errors", func(t *testing.T) {
		var got *UserError
		joined := errors.Join(errors.New("other"), target)
		if !As(joined, &got) || got != target {
			t.Fatalf("As failed through errors.Join: got=%v", got)
		}
	})

	t.Run("other target types are supported", func(t *testing.T) {
		var pe *customErr
		wrapped := fmt.Errorf("ctx: %w", &customErr{code: 7})
		if !As(wrapped, &pe) {
			t.Fatal("As only worked for *UserError targets")
		}
		if pe.code != 7 {
			t.Fatalf("code = %d, want 7", pe.code)
		}
	})

	t.Run("no match", func(t *testing.T) {
		var got *UserError
		if As(errors.New("plain"), &got) {
			t.Fatal("As reported a match for an unrelated error")
		}
	})

	t.Run("bad inputs report false instead of panicking", func(t *testing.T) {
		var got *UserError
		if As(nil, &got) {
			t.Fatal("As(nil, target) reported a match")
		}
		if As(error(target), nil) {
			t.Fatal("As(err, nil) reported a match")
		}
		notAnErrorPointer := 0
		if As(error(target), &notAnErrorPointer) {
			t.Fatal("As accepted a non-error target")
		}
	})
}

type customErr struct{ code int }

func (c *customErr) Error() string { return fmt.Sprintf("custom %d", c.code) }

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantMsg       string
		wantRetryable bool
	}{
		{name: "timeout", err: errors.New("context deadline exceeded"), wantMsg: "Request timed out", wantRetryable: true},
		{name: "cancelled", err: errors.New("context canceled"), wantMsg: "Request cancelled", wantRetryable: true},
		{name: "network", err: errors.New("dial tcp 1.2.3.4:443: connection refused"), wantMsg: "Cannot reach the server", wantRetryable: true},
		{name: "auth", err: errors.New("401 unauthorized"), wantMsg: "Authentication failed"},
		{name: "rate limit", err: errors.New("429 rate limit"), wantMsg: "Rate limit exceeded", wantRetryable: true},
		{name: "unknown", err: errors.New("something odd"), wantMsg: "Something went wrong", wantRetryable: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ue := ClassifyError(tc.err)
			if ue == nil {
				t.Fatal("ClassifyError returned nil for a non-nil error")
			}
			if ue.UserMsg != tc.wantMsg {
				t.Fatalf("UserMsg = %q, want %q", ue.UserMsg, tc.wantMsg)
			}
			if ue.Retryable != tc.wantRetryable {
				t.Fatalf("Retryable = %v, want %v", ue.Retryable, tc.wantRetryable)
			}
			if !errors.Is(ue, ue) || !errors.Is(error(ue), tc.err) {
				t.Fatalf("classified error lost the original cause: %v", ue.Unwrap())
			}
		})
	}

	if ClassifyError(nil) != nil {
		t.Fatal("ClassifyError(nil) must be nil")
	}
}

// A wrapped, already-classified error must classify to the same UserError
// instead of being reclassified as a generic failure.
func TestClassifyErrorIsIdempotentThroughWrapping(t *testing.T) {
	first := ClassifyError(errors.New("429 rate limit"))
	wrapped := fmt.Errorf("calling model: %w", first)

	again := ClassifyError(wrapped)
	if again != first {
		t.Fatalf("reclassified a wrapped UserError: got %+v, want %+v", again, first)
	}
}
