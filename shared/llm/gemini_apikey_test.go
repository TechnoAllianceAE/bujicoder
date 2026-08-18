package llm

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// captureTransport records the request it was handed and optionally fails, so
// tests can inspect the outgoing URL/headers and the error the client produces.
type captureTransport struct {
	req     *http.Request
	failErr error
	resp    *http.Response
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.req = req
	if c.failErr != nil {
		return nil, c.failErr
	}
	return c.resp, nil
}

const testGeminiKey = "AIzaSy-super-secret-key-value"

// TestGeminiAPIKeyNotInURL is the credential-leak regression. Passing the key as
// a ?key= query parameter puts it in the request URL, and net/http wraps every
// transport failure in *url.Error whose Error() prints that URL — so the key
// ends up in logs and in the TUI error banner on any network hiccup.
func TestGeminiAPIKeyNotInURL(t *testing.T) {
	t.Run("key travels in the x-goog-api-key header, not the URL", func(t *testing.T) {
		tr := &captureTransport{resp: &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}}
		g := NewGeminiProvider(testGeminiKey)
		g.client = &http.Client{Transport: tr}

		ch, err := g.StreamCompletion(context.Background(), &CompletionRequest{Model: "gemini-2.5-pro"})
		if err != nil {
			t.Fatalf("StreamCompletion: %v", err)
		}
		drain(t, ch)

		if tr.req == nil {
			t.Fatal("transport never saw a request")
		}
		full := tr.req.URL.String()
		if strings.Contains(full, testGeminiKey) {
			t.Fatalf("api key present in request URL: %s", full)
		}
		if tr.req.URL.Query().Has("key") {
			t.Fatalf("request URL still carries a key query parameter: %s", full)
		}
		if got := tr.req.Header.Get("x-goog-api-key"); got != testGeminiKey {
			t.Fatalf("x-goog-api-key header = %q, want the api key", got)
		}
	})

	t.Run("transport errors do not leak the key", func(t *testing.T) {
		// http.Client wraps this in *url.Error, which prints the request URL.
		tr := &captureTransport{failErr: fmt.Errorf("dial tcp: connection refused")}
		g := NewGeminiProvider(testGeminiKey)
		g.client = &http.Client{Transport: tr}

		_, err := g.StreamCompletion(context.Background(), &CompletionRequest{Model: "gemini-2.5-pro"})
		if err == nil {
			t.Fatal("expected a transport error")
		}
		if strings.Contains(err.Error(), testGeminiKey) {
			t.Fatalf("api key leaked into the error message: %v", err)
		}
	})

	t.Run("non-2xx errors do not leak the key", func(t *testing.T) {
		tr := &captureTransport{resp: &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}}
		g := NewGeminiProvider(testGeminiKey)
		g.client = &http.Client{Transport: tr}

		_, err := g.StreamCompletion(context.Background(), &CompletionRequest{Model: "gemini-2.5-pro"})
		if err == nil {
			t.Fatal("expected a provider error for a 403")
		}
		if strings.Contains(err.Error(), testGeminiKey) {
			t.Fatalf("api key leaked into the provider error: %v", err)
		}
	})
}
