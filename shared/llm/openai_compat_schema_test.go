package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatSendsResponseFormat(t *testing.T) {
	for _, kind := range []string{"json_schema", "json_object", ""} {
		t.Run(kind, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"{}\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
			}))
			defer srv.Close()
			p := newOpenAICompatProvider(OpenAICompatConfig{APIURL: srv.URL, ProviderName: "openai"})
			req := &CompletionRequest{Model: "test"}
			if kind != "" {
				req.ResponseFormat = &ResponseFormat{Type: kind, Schema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}
			}
			ch, err := p.streamCompletion(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			for range ch {
			}
			body := <-requests
			if kind == "" {
				if _, ok := body["response_format"]; ok {
					t.Fatal("ordinary requests changed")
				}
				return
			}
			format, ok := body["response_format"].(map[string]any)
			if !ok || format["type"] != kind {
				t.Fatalf("format = %#v", body["response_format"])
			}
			if kind == "json_schema" {
				schema, ok := format["json_schema"].(map[string]any)
				if !ok || schema["strict"] != true || schema["name"] != "structured_output" {
					t.Fatalf("schema = %#v", format)
				}
				if schema["schema"].(map[string]any)["type"] != "object" {
					t.Fatal("schema was not forwarded")
				}
			} else if _, ok := format["json_schema"]; ok {
				t.Fatal("json_object contains schema")
			}
		})
	}
}

func TestOpenRouterSendsResponseFormat(t *testing.T) {
	req := &CompletionRequest{Model: "fixture", ResponseFormat: &ResponseFormat{Type: "json_schema", Schema: map[string]any{"type": "object"}}}
	provider := NewOpenRouterProvider("synthetic-key")
	body := provider.buildRequest(req)
	format, ok := body["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("format = %#v", body["response_format"])
	}
	schema, ok := format["json_schema"].(map[string]any)
	if !ok || schema["strict"] != true || schema["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("schema = %#v", format)
	}
}
