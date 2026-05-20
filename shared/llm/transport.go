package llm

import (
	"net/http"
	"time"
)

// newPooledTransport creates an http.Transport tuned for high-concurrency
// streaming connections to upstream LLM providers.
//
// Go's default http.Transport sets MaxIdleConnsPerHost=2, which forces
// constant TLS re-handshakes under concurrent load (>2 simultaneous
// requests to the same upstream host, e.g. api.anthropic.com).
// Each TLS 1.3 handshake adds ~150–300ms to Time-to-First-Token.
//
// This transport:
//   - Allows up to 50 idle connections per host (reusing warm TLS sessions)
//   - Allows up to 200 idle connections globally
//   - Keeps idle connections alive for 120s (longer than the 90s default)
//   - Forces HTTP/2 negotiation for multiplexing over a single TCP connection
//
// The headerTimeout parameter bounds the connect+headers phase only; the
// streaming body is bounded by the request context, not by http.Client.Timeout.
func newPooledTransport(headerTimeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: headerTimeout,
		IdleConnTimeout:       120 * time.Second,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		ForceAttemptHTTP2:     true,
	}
}
