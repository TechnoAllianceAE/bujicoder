package llm

import (
	"fmt"
	"testing"
	"time"
)

func TestCalculateRetryDelay_ExponentialBackoff(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay:  1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
	}

	for attempt := range 5 {
		delay := calculateRetryDelay(attempt, cfg)
		if delay <= 0 {
			t.Errorf("attempt %d: delay must be positive, got %v", attempt, delay)
		}
		maxWithJitter := time.Duration(float64(cfg.MaxDelay) * 1.25)
		if delay > maxWithJitter {
			t.Errorf("attempt %d: delay %v exceeds max+jitter %v", attempt, delay, maxWithJitter)
		}
	}
}

func TestCalculateRetryDelay_RespectsMaxDelay(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay:  1 * time.Second,
		MaxDelay:      5 * time.Second,
		BackoffFactor: 10.0,
	}

	delay := calculateRetryDelay(10, cfg)
	maxWithJitter := time.Duration(float64(cfg.MaxDelay) * 1.25)
	if delay > maxWithJitter {
		t.Errorf("delay %v exceeds MaxDelay with jitter %v", delay, maxWithJitter)
	}
}

func TestCalculateRetryDelay_MinimumFloor(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay:  1 * time.Millisecond,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 1.0,
	}

	for range 100 {
		delay := calculateRetryDelay(0, cfg)
		if delay < 100*time.Millisecond {
			t.Errorf("delay %v is below 100ms floor", delay)
		}
	}
}

func TestCalculateRetryDelay_AlwaysPositive(t *testing.T) {
	cfg := RetryConfig{
		InitialDelay:  100 * time.Millisecond,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
	}

	for i := range 1000 {
		for attempt := range 5 {
			delay := calculateRetryDelay(attempt, cfg)
			if delay <= 0 {
				t.Fatalf("attempt %d, iteration %d: got non-positive delay %v", attempt, i, delay)
			}
		}
	}
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		errMsg string
		want   bool
	}{
		{"API error (status 429): rate limited", true},
		{"HTTP 500: internal server error", true},
		{"bad gateway (502)", true},
		{"service unavailable 503", true},
		{"error 529: overloaded", true},
		{"invalid API key", false},
		{"context deadline exceeded", false},
	}

	for _, tt := range tests {
		t.Run(tt.errMsg, func(t *testing.T) {
			got := isRetryableError(fmt.Errorf("%s", tt.errMsg))
			if got != tt.want {
				t.Errorf("isRetryableError(%q) = %v, want %v", tt.errMsg, got, tt.want)
			}
		})
	}
}

func TestIsOverloadedError(t *testing.T) {
	if !isOverloadedError(fmt.Errorf("529 overloaded")) {
		t.Error("should detect 529")
	}
	if isOverloadedError(fmt.Errorf("429 rate limited")) {
		t.Error("should not flag 429 as overloaded")
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()
	if cfg.MaxRetries != 3 {
		t.Errorf("MaxRetries = %d, want 3", cfg.MaxRetries)
	}
	if cfg.InitialDelay != 1*time.Second {
		t.Errorf("InitialDelay = %v, want 1s", cfg.InitialDelay)
	}
}
