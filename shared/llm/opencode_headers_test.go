package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OpenCode Go rejects requests without x-opencode-session (400
// MissingSessionID) and Cloudflare refuses generic library user agents, so we
// identify as the official CLI.
func TestOpenCodeSendsSessionAndUserAgent(t *testing.T) {
	var sessions, agents, clients, reqIDs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get("x-opencode-session"))
		agents = append(agents, r.Header.Get("User-Agent"))
		clients = append(clients, r.Header.Get("x-opencode-client"))
		reqIDs = append(reqIDs, r.Header.Get("x-opencode-request-id"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := newOpenCodeWithBase(t, "opencode", "go", srv.URL, "k")
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

	if sessions[0] == "" || !strings.HasPrefix(sessions[0], "ses_") {
		t.Fatalf("session header = %q, want ses_ prefix", sessions[0])
	}
	assertOpenCodeIDShape(t, "session", strings.TrimPrefix(sessions[0], "ses_"))
	if sessions[0] != sessions[1] {
		t.Errorf("session changed within one conversation: %q vs %q", sessions[0], sessions[1])
	}
	if !strings.HasPrefix(agents[0], "opencode/") {
		t.Errorf("User-Agent = %q, want official opencode client UA", agents[0])
	}
	if clients[0] != "cli" {
		t.Errorf("x-opencode-client = %q, want cli", clients[0])
	}
	for i, id := range reqIDs {
		if !strings.HasPrefix(id, "msg_") {
			t.Errorf("x-opencode-request-id[%d] = %q, want msg_ prefix", i, id)
		} else {
			assertOpenCodeIDShape(t, "request-id", strings.TrimPrefix(id, "msg_"))
		}
	}
	if len(reqIDs) == 2 && reqIDs[0] == reqIDs[1] {
		t.Errorf("x-opencode-request-id reused across requests: %q", reqIDs[0])
	}
}

// assertOpenCodeIDShape checks the CLI id payload shape: 12 lowercase hex
// chars (snowflake timestamp) + 14 base62 chars, e.g.
// f0959c456ffeQ7NLV6sEiokigu.
func assertOpenCodeIDShape(t *testing.T, what, payload string) {
	t.Helper()
	const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	if len(payload) != 26 {
		t.Fatalf("%s payload = %q, want 26 chars", what, payload)
	}
	for i, c := range payload {
		if i < 12 {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("%s payload = %q, char %d not lowercase hex", what, payload, i)
			}
		} else if !strings.ContainsRune(base62, c) {
			t.Fatalf("%s payload = %q, char %d not base62", what, payload, i)
		}
	}
}

// newOpenCodeWithBase points both tier bases at a test server and builds a
// provider for the given tier.
func newOpenCodeWithBase(t *testing.T, name, tier, base, key string) *OpenCodeProvider {
	t.Helper()
	t.Setenv("OPENCODE_ZEN_BASE_URL", base)
	t.Setenv("OPENCODE_GO_BASE_URL", base)
	t.Setenv("OPENCODE_API_VERSION", "v1")
	return newOpenCode(name, tier, key)
}

func TestOpenCodeSessionFallsBackToRequestID(t *testing.T) {
	if got := opencodeSession(&CompletionRequest{RequestID: "req-7"}); got != "req-7" {
		t.Errorf("got %q, want the request ID", got)
	}
}

func TestOpenCodeEndpointsConfigurable(t *testing.T) {
	t.Setenv("OPENCODE_API_VERSION", "v9")
	t.Setenv("OPENCODE_ZEN_BASE_URL", "https://zen.example.com/custom")
	t.Setenv("OPENCODE_GO_BASE_URL", "https://go.example.com/custom/")
	t.Setenv("OPENCODE_USER_AGENT", "test-agent/1.0")

	if got := OpenCodeChatURL("zen"); got != "https://zen.example.com/custom/v9/chat/completions" {
		t.Errorf("zen chat URL = %q", got)
	}
	if got := OpenCodeChatURL("go"); got != "https://go.example.com/custom/v9/chat/completions" {
		t.Errorf("go chat URL = %q", got)
	}
	if got := OpenCodeModelsURL("zen"); got != "https://zen.example.com/custom/v9/models" {
		t.Errorf("zen models URL = %q", got)
	}
	if got := OpenCodeSystemOneURL("go"); got != "https://go.example.com/custom/v9/systemone" {
		t.Errorf("go systemone URL = %q", got)
	}
	if got := OpenCodeUserAgent(); got != "test-agent/1.0" {
		t.Errorf("user agent = %q", got)
	}
}

func TestOpenCodeEndpointsDefault(t *testing.T) {
	t.Setenv("OPENCODE_API_VERSION", "")
	t.Setenv("OPENCODE_ZEN_BASE_URL", "")
	t.Setenv("OPENCODE_GO_BASE_URL", "")
	t.Setenv("OPENCODE_USER_AGENT", "")

	if got := OpenCodeChatURL("zen"); got != "https://opencode.ai/zen/v1/chat/completions" {
		t.Errorf("zen chat URL = %q", got)
	}
	if got := OpenCodeChatURL("go"); got != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Errorf("go chat URL = %q", got)
	}
	if got := OpenCodeUserAgent(); got != opencodeUserAgent {
		t.Errorf("user agent = %q", got)
	}
}
