package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OpenCode Go rejects requests without x-opencode-session (400
// MissingSessionID) and Cloudflare refuses generic library user agents.
func TestOpenCodeSendsSessionAndUserAgent(t *testing.T) {
	var sessions, agents []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		agents = append(agents, r.Header.Get("User-Agent"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := newOpenCode("opencode", srv.URL, "k")
	first := Message{Role: "user", Content: []ContentPart{{Type: "text", Text: "fix the bug"}}}
	turn := func(msgs ...Message) {
		ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{RequestID: "req-x", UserID: "u1", Model: "m", Messages: msgs})
		if err != nil {
			t.Fatal(err)
		}
		for range ch {
		}
	}
	turn(first)
	turn(first,
		Message{Role: "assistant", Content: []ContentPart{{Type: "text", Text: "done"}}},
		Message{Role: "user", Content: []ContentPart{{Type: "text", Text: "now commit"}}})

	if sessions[0] == "" || !strings.HasPrefix(sessions[0], "bc-") {
		t.Fatalf("session header = %q", sessions[0])
	}
	if sessions[0] != sessions[1] {
		t.Errorf("session changed within one conversation: %q vs %q", sessions[0], sessions[1])
	}
	if !strings.HasPrefix(agents[0], "bujicoder/") {
		t.Errorf("User-Agent = %q", agents[0])
	}
}

func TestOpenCodeSessionFallsBackToRequestID(t *testing.T) {
	if got := opencodeSession(&CompletionRequest{RequestID: "req-7"}); got != "req-7" {
		t.Errorf("got %q, want the request ID", got)
	}
}
