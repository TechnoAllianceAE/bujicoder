package llm

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// RetryConfig configures retry behavior for LLM provider calls.
type RetryConfig struct {
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	FallbackModel string // Model to try on 529 (overloaded)
}

// DefaultRetryConfig returns sensible retry defaults.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:    3,
		InitialDelay:  1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
	}
}

// retryProvider wraps a Provider with automatic retry on transient errors.
type retryProvider struct {
	inner Provider
	cfg   RetryConfig
}

// WithRetry wraps a provider with retry logic. Transient errors (429, 500, 502,
// 503, 529) are retried with exponential backoff and jitter.
func WithRetry(p Provider, cfg RetryConfig) Provider {
	return &retryProvider{inner: p, cfg: cfg}
}

func (r *retryProvider) Name() string { return r.inner.Name() }

func (r *retryProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	var lastErr error

	for attempt := 0; attempt <= r.cfg.MaxRetries; attempt++ {
		ch, err := r.inner.StreamCompletion(ctx, req)
		if err == nil {
			return ch, nil
		}

		if !isRetryableError(err) {
			return nil, err
		}

		lastErr = err

		if attempt >= r.cfg.MaxRetries {
			break
		}

		// Handle 529 (overloaded) with model fallback
		if r.cfg.FallbackModel != "" && isOverloadedError(err) {
			req.Model = r.cfg.FallbackModel
		}

		delay := calculateRetryDelay(attempt, r.cfg)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", r.cfg.MaxRetries, lastErr)
}

// calculateRetryDelay computes the delay for a retry attempt with exponential
// backoff and jitter. Always returns at least 100ms.
func calculateRetryDelay(attempt int, cfg RetryConfig) time.Duration {
	delay := float64(cfg.InitialDelay) * math.Pow(cfg.BackoffFactor, float64(attempt))
	if delay > float64(cfg.MaxDelay) {
		delay = float64(cfg.MaxDelay)
	}

	// Add jitter (±25%), clamp to 100ms minimum
	jitter := delay * 0.25 * (rand.Float64()*2 - 1)
	delay += jitter
	if delay < float64(100*time.Millisecond) {
		delay = float64(100 * time.Millisecond)
	}

	return time.Duration(delay)
}

// isRetryableError checks if an error is transient and worth retrying.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	retryablePatterns := []string{
		"429", "rate limit",
		"500", "internal server",
		"502", "bad gateway",
		"503", "service unavailable",
		"529", "overloaded",
	}
	for _, p := range retryablePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// isOverloadedError checks for 529 (overloaded) specifically.
func isOverloadedError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "529") || strings.Contains(msg, "overloaded")
}
