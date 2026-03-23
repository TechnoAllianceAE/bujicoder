package llm

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProviderError represents a structured error from an LLM provider.
// It includes the HTTP status code, message, optional Retry-After duration,
// and whether the error is retryable (transient vs permanent).
type ProviderError struct {
	StatusCode int
	Message    string
	RetryAfter time.Duration
	Retryable  bool
}

// Error implements the error interface.
func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error (status %d): %s", e.StatusCode, e.Message)
}

// IsRetryable returns true if err is a ProviderError with a retryable status code.
func IsRetryable(err error) bool {
	if pe, ok := err.(*ProviderError); ok {
		return pe.Retryable
	}
	return false
}

// RetryAfterDuration extracts the Retry-After duration from err if it's a ProviderError.
func RetryAfterDuration(err error) time.Duration {
	if pe, ok := err.(*ProviderError); ok {
		return pe.RetryAfter
	}
	return 0
}

// parseRetryAfter extracts the Retry-After duration from response headers.
// Supports:
// - Retry-After: <seconds>
// - Retry-After: <HTTP-date>
// - x-ms-retry-after-ms: <milliseconds>
// - retry-after-ms: <milliseconds>
func parseRetryAfter(headers map[string]string) time.Duration {
	// Try x-ms-retry-after-ms first (milliseconds)
	if val, ok := headers["x-ms-retry-after-ms"]; ok {
		if ms, err := strconv.Atoi(val); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}

	// Try retry-after-ms (milliseconds)
	if val, ok := headers["retry-after-ms"]; ok {
		if ms, err := strconv.Atoi(val); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}

	// Try standard Retry-After header (seconds or HTTP-date)
	if val, ok := headers["retry-after"]; ok {
		// Try as seconds (integer)
		if seconds, err := strconv.Atoi(val); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		// Could also try parsing as HTTP-date, but seconds is most common
	}

	return 0
}

// NewProviderError creates a ProviderError from HTTP response status and body text.
// It automatically determines if the error is retryable based on status code.
func NewProviderError(statusCode int, message string, retryAfterDur time.Duration) *ProviderError {
	retryable := isRetryableStatus(statusCode)
	return &ProviderError{
		StatusCode: statusCode,
		Message:    message,
		RetryAfter: retryAfterDur,
		Retryable:  retryable,
	}
}

// isRetryableStatus returns true for transient error status codes.
func isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case 429: // Too Many Requests
		fallthrough
	case 500: // Internal Server Error
		fallthrough
	case 502: // Bad Gateway
		fallthrough
	case 503: // Service Unavailable
		fallthrough
	case 504: // Gateway Timeout
		return true
	default:
		return false
	}
}

// ExtractRetryAfterFromHeaders parses all Retry-After variants from a header map.
// The map key should be normalized to lowercase.
func ExtractRetryAfterFromHeaders(headers map[string]string) time.Duration {
	return parseRetryAfter(headers)
}

// NormalizeHeaders converts header names to lowercase for consistent lookup.
func NormalizeHeaders(headers map[string][]string) map[string]string {
	normalized := make(map[string]string)
	for k, v := range headers {
		if len(v) > 0 {
			normalized[strings.ToLower(k)] = v[0]
		}
	}
	return normalized
}
