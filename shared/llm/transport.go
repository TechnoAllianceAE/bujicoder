package llm

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"sync"
	"time"
)

// maxErrorBodyBytes bounds how much of a non-2xx response body is kept for the
// error message. Provider error payloads are small JSON documents; a hostile
// or misconfigured upstream (e.g. an HTML error page from a proxy) must not be
// able to make us buffer an unbounded amount of memory.
const maxErrorBodyBytes = 64 << 10

// maxErrorDrainBytes bounds how much of the remaining error body is drained
// after the message has been captured. Draining lets the pooled keep-alive
// connection be reused instead of being torn down, but must itself be bounded.
const maxErrorDrainBytes = 256 << 10

// readErrorBody reads a bounded prefix of an error response body for use in an
// error message, drains a bounded remainder so the connection can return to
// the idle pool, and closes the body. Always call this instead of
// io.ReadAll(resp.Body) on a non-2xx path.
func readErrorBody(resp *http.Response) string {
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorDrainBytes))
	return string(data)
}

// sendEvent delivers ev on ch, aborting if ctx is done. It reports whether the
// event was delivered; a false result means the request was canceled and the
// producing goroutine must return.
//
// Every stream producer must send through this helper. The event channel is
// buffered but finite, so a consumer that stops reading (cancellation, or an
// early return out of its `range` loop) would otherwise block the producer
// forever, leaking the goroutine, the response body and its socket.
func sendEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// transportCache holds shared *http.Transport instances keyed by header
// timeout. Multiple providers (and multiple API keys for the same provider)
// that share a header timeout reuse one transport, so connections to a given
// upstream host are pooled across all of them instead of each provider
// keeping its own isolated pool. http.Transport maintains per-host idle pools
// internally, so a single transport safely serves several upstream hosts.
var transportCache sync.Map // map[time.Duration]*http.Transport

// sharedPooledTransport returns a process-wide transport for the given header
// timeout, creating it once on first use. Prefer this over newPooledTransport
// in provider constructors so connection reuse spans providers/keys.
func sharedPooledTransport(headerTimeout time.Duration) *http.Transport {
	if v, ok := transportCache.Load(headerTimeout); ok {
		return v.(*http.Transport)
	}
	actual, _ := transportCache.LoadOrStore(headerTimeout, newPooledTransport(headerTimeout))
	return actual.(*http.Transport)
}

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
