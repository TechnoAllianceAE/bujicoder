package llm

import (
	"crypto/tls"
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
//   - Forces HTTP/1.1 for upstream streaming (see below)
//
// HTTP/2 is explicitly disabled via a non-nil empty TLSNextProto. Go enables
// HTTP/2 by default when no custom Dial/TLS fields are set, but HTTP/2 stream
// resets (GOAWAY / RST_STREAM) on pooled connections surface mid-stream as
// "stream closed before response.completed" for SSE clients (Codex, Claude
// Code). HTTP/1.1 with keep-alive gives the same connection-reuse win without
// the multiplexed-stream-reset failure mode.
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
		// Non-nil empty map disables automatic HTTP/2 negotiation.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
}
