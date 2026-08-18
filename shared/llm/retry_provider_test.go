package llm

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedProvider returns a pre-programmed error (or success) per attempt and
// records the model each attempt was made with.
type scriptedProvider struct {
	errs   []error
	calls  atomic.Int32
	models []string
}

func (s *scriptedProvider) Name() string { return "scripted" }

func (s *scriptedProvider) StreamCompletion(_ context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	i := int(s.calls.Add(1)) - 1
	s.models = append(s.models, req.Model)
	if i < len(s.errs) && s.errs[i] != nil {
		return nil, s.errs[i]
	}
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Complete: &CompleteEvent{FinishReason: "stop"}}
	close(ch)
	return ch, nil
}

func fastRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:    3,
		InitialDelay:  time.Millisecond,
		MaxDelay:      5 * time.Millisecond,
		BackoffFactor: 2.0,
	}
}

// TestRetryStatusClassification pins which provider statuses are retried. The
// status carried by *ProviderError must decide, not substring matching on the
// upstream's message: a fatal 400 whose body quotes a rate-limit hint or a
// token count of 500 must fail immediately instead of being retried.
func TestRetryStatusClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantCalls int32
	}{
		{"429 is retried", NewProviderError(429, "slow down", 0), 4},
		{"408 is retried", NewProviderError(408, "request timeout", 0), 4},
		{"529 overloaded is retried", NewProviderError(529, "overloaded", 0), 4},
		{"503 is retried", NewProviderError(503, "unavailable", 0), 4},
		{"401 is fatal", NewProviderError(401, "invalid api key", 0), 1},
		{"404 is fatal", NewProviderError(404, "no such model", 0), 1},
		{"400 quoting a rate limit is fatal", NewProviderError(400, "max_tokens 500 exceeds rate limit", 0), 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &scriptedProvider{errs: []error{tt.err, tt.err, tt.err, tt.err}}
			p := WithRetry(inner, fastRetryConfig())

			if _, err := p.StreamCompletion(context.Background(), &CompletionRequest{Model: "m"}); err == nil {
				t.Fatal("expected an error")
			}
			if got := inner.calls.Load(); got != tt.wantCalls {
				t.Fatalf("attempts = %d, want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestRetrySucceedsAfterTransientError guards the happy path: a transient
// failure must not surface to the caller.
func TestRetrySucceedsAfterTransientError(t *testing.T) {
	inner := &scriptedProvider{errs: []error{NewProviderError(503, "unavailable", 0), nil}}
	p := WithRetry(inner, fastRetryConfig())

	ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	if !hasComplete(drain(t, ch)) {
		t.Fatal("expected the second attempt's Complete event")
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

// TestRetryHonorsRetryAfter checks that a server-supplied Retry-After drives the
// wait (instead of being ignored in favour of blind exponential backoff) while
// still being clamped so a bogus header cannot stall the request.
func TestRetryHonorsRetryAfter(t *testing.T) {
	t.Run("uses the header value", func(t *testing.T) {
		inner := &scriptedProvider{errs: []error{NewProviderError(429, "slow down", 300*time.Millisecond), nil}}
		cfg := RetryConfig{MaxRetries: 2, InitialDelay: time.Millisecond, MaxDelay: 500 * time.Millisecond, BackoffFactor: 2}

		start := time.Now()
		if _, err := WithRetry(inner, cfg).StreamCompletion(context.Background(), &CompletionRequest{Model: "m"}); err != nil {
			t.Fatalf("StreamCompletion: %v", err)
		}
		elapsed := time.Since(start)
		// The 100ms backoff floor (jittered) can never reach ~300ms, so this
		// distinguishes honoring the header from ignoring it.
		if elapsed < 250*time.Millisecond {
			t.Fatalf("waited %v, expected to honor the 300ms Retry-After", elapsed)
		}
	})

	t.Run("clamps an absurd header to MaxDelay", func(t *testing.T) {
		err := NewProviderError(429, "slow down", time.Hour)
		cfg := RetryConfig{MaxRetries: 1, InitialDelay: time.Millisecond, MaxDelay: 20 * time.Millisecond, BackoffFactor: 2}
		if got := retryDelay(0, err, cfg); got != cfg.MaxDelay {
			t.Fatalf("delay = %v, want the %v clamp", got, cfg.MaxDelay)
		}
	})

	t.Run("falls back to backoff without a header", func(t *testing.T) {
		cfg := fastRetryConfig()
		if got := retryDelay(0, NewProviderError(429, "slow down", 0), cfg); got <= 0 {
			t.Fatalf("delay = %v, want a positive backoff", got)
		}
	})
}

// TestRetryHonorsContextCancellation is the regression for a retry loop that
// sleeps through cancellation: the caller must get ctx.Err() promptly and the
// inner provider must not be called again.
func TestRetryHonorsContextCancellation(t *testing.T) {
	inner := &scriptedProvider{errs: []error{
		NewProviderError(429, "slow down", 30*time.Second),
		NewProviderError(429, "slow down", 30*time.Second),
	}}
	cfg := RetryConfig{MaxRetries: 3, InitialDelay: 30 * time.Second, MaxDelay: 30 * time.Second, BackoffFactor: 2}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := WithRetry(inner, cfg).StreamCompletion(ctx, &CompletionRequest{Model: "m"})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("returned after %v; the retry sleep ignored cancellation", elapsed)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 (no attempt after cancellation)", got)
	}
}

// TestRetryFallbackModelDoesNotMutateRequest covers the 529 fallback: the
// fallback must apply to the retry attempt only. Mutating the caller's
// CompletionRequest pins every later turn of the conversation to the fallback
// model even after the primary recovers.
func TestRetryFallbackModelDoesNotMutateRequest(t *testing.T) {
	inner := &scriptedProvider{errs: []error{NewProviderError(529, "overloaded", 0), nil}}
	cfg := fastRetryConfig()
	cfg.FallbackModel = "cheap-model"

	req := &CompletionRequest{Model: "primary-model"}
	if _, err := WithRetry(inner, cfg).StreamCompletion(context.Background(), req); err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}

	if req.Model != "primary-model" {
		t.Fatalf("caller's request model mutated to %q", req.Model)
	}
	want := []string{"primary-model", "cheap-model"}
	if len(inner.models) != len(want) {
		t.Fatalf("attempt models = %v, want %v", inner.models, want)
	}
	for i, m := range want {
		if inner.models[i] != m {
			t.Fatalf("attempt %d used model %q, want %q", i, inner.models[i], m)
		}
	}
}

// TestIsRetryableWrappedError guards the classification helpers against wrapped
// errors — the retry wrapper itself wraps with %w.
func TestIsRetryableWrappedError(t *testing.T) {
	base := NewProviderError(503, "unavailable", 7*time.Second)
	wrapped := fmt.Errorf("start completion: %w", base)

	if !IsRetryable(wrapped) {
		t.Error("IsRetryable should see through a wrapped ProviderError")
	}
	if got := RetryAfterDuration(wrapped); got != 7*time.Second {
		t.Errorf("RetryAfterDuration = %v, want 7s", got)
	}
	if IsRetryable(fmt.Errorf("plain failure")) {
		t.Error("a non-provider error must not be retryable")
	}
}
