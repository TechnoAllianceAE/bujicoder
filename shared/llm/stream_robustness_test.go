package llm

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// sseWriter writes an SSE response and flushes each write so the client sees a
// real incremental stream.
func sseHandler(write func(w io.Writer, flush func())) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write(w, func() {
			if flusher != nil {
				flusher.Flush()
			}
		})
	}
}

func compatProvider(url string) *openAICompatProvider {
	return newOpenAICompatProvider(OpenAICompatConfig{APIURL: url, ProviderName: "test"})
}

func drain(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	return events
}

func findError(events []StreamEvent, code string) *ErrorEvent {
	for _, ev := range events {
		if ev.Error != nil && ev.Error.Code == code {
			return ev.Error
		}
	}
	return nil
}

func hasComplete(events []StreamEvent) bool {
	for _, ev := range events {
		if ev.Complete != nil {
			return true
		}
	}
	return false
}

// TestErrorResponseBody covers the leaked-body-on-error path: a non-2xx body
// must never be buffered without a limit, and a normal-sized error body must be
// drained and closed so the pooled connection stays reusable.
func TestErrorResponseBody(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantConns int
	}{
		{
			name:      "typical provider error json is drained and the connection pooled",
			body:      `{"error":{"message":"model not found","type":"invalid_request_error"}}`,
			wantConns: 1,
		},
		{
			// 4 MiB HTML error page from an upstream proxy. Reading it whole
			// would let the upstream dictate our memory use.
			name:      "oversized error page is truncated",
			body:      strings.Repeat("x", 4<<20),
			wantConns: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newConns := make(chan struct{}, 16)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, tt.body)
			}))
			srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					select {
					case newConns <- struct{}{}:
					default:
					}
				}
			}
			srv.Start()
			defer srv.Close()

			p := compatProvider(srv.URL)
			req := &CompletionRequest{Model: "m"}

			for attempt := range 3 {
				ch, err := p.streamCompletion(context.Background(), req)
				if err == nil {
					t.Fatalf("attempt %d: expected an error for a 500 response", attempt)
				}
				if ch != nil {
					t.Fatalf("attempt %d: expected a nil channel alongside the error", attempt)
				}
				var pe *ProviderError
				if !errors.As(err, &pe) {
					t.Fatalf("attempt %d: expected *ProviderError, got %T", attempt, err)
				}
				if len(pe.Message) > maxErrorBodyBytes {
					t.Fatalf("attempt %d: error body not bounded: %d bytes (max %d)",
						attempt, len(pe.Message), maxErrorBodyBytes)
				}
			}

			if got := len(newConns); got != tt.wantConns {
				t.Fatalf("connections opened for 3 failed requests = %d, want %d", got, tt.wantConns)
			}
		})
	}
}

// TestOversizedSSELine guards the scanner buffer. bufio.Scanner defaults to a
// 64 KiB token limit; a long tool-call chunk would otherwise abort the stream
// with bufio.Scanner: token too long.
func TestOversizedSSELine(t *testing.T) {
	tests := []struct {
		name         string
		payloadBytes int
		wantText     bool
		wantTruncErr bool
	}{
		{name: "above default 64KiB scanner limit", payloadBytes: 512 << 10, wantText: true},
		{name: "above configured 1MiB limit reports truncation", payloadBytes: 2 << 20, wantTruncErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := strings.Repeat("a", tt.payloadBytes)
			srv := httptest.NewServer(sseHandler(func(w io.Writer, flush func()) {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n", text)
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n")
				fmt.Fprint(w, "data: [DONE]\n")
				flush()
			}))
			defer srv.Close()

			ch, err := compatProvider(srv.URL).streamCompletion(context.Background(), &CompletionRequest{Model: "m"})
			if err != nil {
				t.Fatalf("streamCompletion: %v", err)
			}
			events := drain(t, ch)

			var got string
			for _, ev := range events {
				if ev.Delta != nil {
					got += ev.Delta.Text
				}
			}
			if tt.wantText {
				if got != text {
					t.Fatalf("delta text: got %d bytes, want %d", len(got), len(text))
				}
				if !hasComplete(events) {
					t.Fatal("expected a Complete event for a long but readable line")
				}
			}
			if tt.wantTruncErr {
				if findError(events, "stream_truncated") == nil {
					t.Fatalf("expected a stream_truncated error for an unreadable line, got %+v", events)
				}
				if hasComplete(events) {
					t.Fatal("a stream that could not be read must not report Complete")
				}
			}
		})
	}
}

// TestTruncatedStreamReportsError covers an upstream connection that dies
// mid-response: the consumer must see stream_truncated instead of a silent,
// apparently successful stream.
func TestTruncatedStreamReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n"
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(chunk), chunk)
		// Abort without the terminating zero-length chunk.
		_ = buf.Flush()
	}))
	defer srv.Close()

	ch, err := compatProvider(srv.URL).streamCompletion(context.Background(), &CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}
	events := drain(t, ch)

	if findError(events, "stream_truncated") == nil {
		t.Fatalf("expected stream_truncated for an aborted stream, got %+v", events)
	}
	if hasComplete(events) {
		t.Fatal("an aborted stream must not report Complete")
	}
}

// TestProcessStreamReturnsOnCanceledContext is the goroutine-leak regression.
// The event channel is finite, so a producer that ignores the request context
// blocks forever on send once the consumer stops reading — holding the response
// body and its socket open for the life of the process.
func TestProcessStreamReturnsOnCanceledContext(t *testing.T) {
	const n = 500
	openAISSE := strings.Repeat("data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n", n)
	anthropicSSE := strings.Repeat("data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"tok\"}}\n", n)
	geminiSSE := strings.Repeat("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"tok\"}]}}]}\n", n)

	processors := map[string]struct {
		process func(ctx context.Context, body io.ReadCloser, ch chan<- StreamEvent)
		payload string
	}{
		"openai_compat": {compatProvider("http://unused").processStream, openAISSE},
		"anthropic":     {NewAnthropicProvider("k").processStream, anthropicSSE},
		"openrouter":    {NewOpenRouterProvider("k").processStream, openAISSE},
		"gemini":        {NewGeminiProvider("k").processStream, geminiSSE},
		"vertex":        {(&VertexProvider{}).processStream, geminiSSE},
	}

	for name, tc := range processors {
		t.Run(name, func(t *testing.T) {
			payload := tc.payload
			process := tc.process
			// Unbuffered and never read: the only way out is the context.
			ch := make(chan StreamEvent)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan struct{})
			go func() {
				process(ctx, io.NopCloser(strings.NewReader(payload)), ch)
				close(done)
			}()

			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("processStream did not return after context cancellation — stream goroutine leaked")
			}
		})
	}
}

// TestContextCancellationAbortsLiveStream checks the end-to-end path: canceling
// mid-stream must terminate the stream instead of running to the upstream's
// completion.
func TestContextCancellationAbortsLiveStream(t *testing.T) {
	handlerDone := make(chan struct{})
	srv := httptest.NewServer(sseHandler(func(w io.Writer, flush func()) {
		defer close(handlerDone)
		for range 10000 {
			if _, err := io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n"); err != nil {
				return
			}
			flush()
			time.Sleep(time.Millisecond)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := compatProvider(srv.URL).streamCompletion(ctx, &CompletionRequest{Model: "m"})
	if err != nil {
		t.Fatalf("streamCompletion: %v", err)
	}

	// Consume one delta, then cancel.
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before delivering any event")
		}
		if ev.Delta == nil {
			t.Fatalf("expected a delta first, got %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first delta")
	}
	cancel()

	closed := make(chan struct{})
	go func() {
		for range ch { //nolint:revive // draining until the producer closes
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("event channel stayed open after cancellation")
	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream handler kept streaming after cancellation")
	}
}

// TestSendEventRespectsContext is the unit-level contract every provider relies
// on for cancellation.
func TestSendEventRespectsContext(t *testing.T) {
	t.Run("delivers while live", func(t *testing.T) {
		ch := make(chan StreamEvent, 1)
		if !sendEvent(context.Background(), ch, StreamEvent{Delta: &DeltaEvent{Text: "x"}}) {
			t.Fatal("sendEvent reported failure on a live context")
		}
		if ev := <-ch; ev.Delta == nil || ev.Delta.Text != "x" {
			t.Fatalf("unexpected event %+v", ev)
		}
	})

	t.Run("aborts when canceled and nobody reads", func(t *testing.T) {
		ch := make(chan StreamEvent) // unbuffered, no reader
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := make(chan bool, 1)
		go func() { result <- sendEvent(ctx, ch, StreamEvent{}) }()
		select {
		case ok := <-result:
			if ok {
				t.Fatal("sendEvent reported delivery on a canceled context")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("sendEvent blocked on a canceled context")
		}
	})
}

// TestEventStreamFrameSizeCap guards against a corrupt Bedrock eventstream
// prelude driving a multi-gigabyte allocation.
func TestEventStreamFrameSizeCap(t *testing.T) {
	// totalLength = 0xFFFFFFF0, headersLength = 0.
	prelude := []byte{0xFF, 0xFF, 0xFF, 0xF0, 0, 0, 0, 0, 0, 0, 0, 0}
	dec := newEventStreamDecoder(bufio.NewReader(strings.NewReader(string(prelude))))
	if _, err := dec.next(); err == nil {
		t.Fatal("expected an error for an oversized frame length")
	} else if !strings.Contains(err.Error(), "frame too large") {
		t.Fatalf("expected a frame-size error, got %v", err)
	}
}
