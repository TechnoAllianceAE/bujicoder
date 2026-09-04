package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
)

// TestCustomCompatModelsSurviveRefresh guards against a regression where
// admin-registered custom OpenAI-compatible providers (e.g. deepseek)
// disappeared from the catalog after the periodic/manual refresh rebuilt the
// model map from scratch. MergeOpenAICompatModels must persist the spec so
// fetchFromAPI re-fetches it on every subsequent refresh.
func TestCustomCompatModelsSurviveRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4-pro","created":1700000000}]}`))
	}))
	defer srv.Close()

	catalog := &ModelCatalog{
		models: make(map[string]ModelInfo),
		source: "dynamic",
		client: srv.Client(),
		log:    zerolog.Nop(),
		stopCh: make(chan struct{}),
	}

	if err := catalog.MergeOpenAICompatModels(context.Background(), "deepseek", srv.URL, "key"); err != nil {
		t.Fatalf("MergeOpenAICompatModels: %v", err)
	}
	if _, ok := catalog.Get("deepseek/deepseek-v4-pro"); !ok {
		t.Fatal("model missing immediately after merge")
	}

	// Simulate a refresh (auto-refresh or admin "Refresh Models"): this
	// rebuilds catalog.models from scratch via fetchFromAPI.
	if err := catalog.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, ok := catalog.Get("deepseek/deepseek-v4-pro"); !ok {
		t.Fatal("custom OpenAI-compatible model dropped after refresh — customCompat not replayed")
	}
}

// TestUpsertCustomCompatReplacesExisting ensures re-registering a provider
// (new base URL or key) updates the stored spec in place rather than
// accumulating duplicate entries that would be fetched redundantly on every
// refresh.
func TestUpsertCustomCompatReplacesExisting(t *testing.T) {
	specs := []customCompatSource{{provider: "deepseek", baseURL: "https://old", apiKey: "a"}}
	specs = upsertCustomCompat(specs, customCompatSource{provider: "deepseek", baseURL: "https://new", apiKey: "b"})
	if len(specs) != 1 {
		t.Fatalf("len = %d, want 1", len(specs))
	}
	if specs[0].baseURL != "https://new" || specs[0].apiKey != "b" {
		t.Fatalf("spec not replaced: %+v", specs[0])
	}
}
