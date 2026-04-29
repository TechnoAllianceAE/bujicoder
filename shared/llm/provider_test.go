package llm

import (
	"context"
	"testing"
)

// mockProvider implements Provider for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) StreamCompletion(ctx context.Context, req *CompletionRequest) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, 1)
	ch <- StreamEvent{Delta: &DeltaEvent{Text: "hello"}}
	close(ch)
	return ch, nil
}

func (m *mockProvider) Name() string { return m.name }

func TestRegistryRoute(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "anthropic"})
	reg.Register(&mockProvider{name: "openai"})

	t.Run("valid route", func(t *testing.T) {
		p, model, err := reg.Route("openai/gpt-oss-120b:free")
		if err != nil {
			t.Fatal(err)
		}
		if p.Name() != "openai" {
			t.Errorf("provider = %q, want %q", p.Name(), "openai")
		}
		if model != "gpt-oss-120b:free" {
			t.Errorf("model = %q, want %q", model, "gpt-oss-120b:free")
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		_, _, err := reg.Route("google/gemini-pro")
		if err == nil {
			t.Fatal("expected error for unknown provider")
		}
	})

	t.Run("invalid format", func(t *testing.T) {
		_, _, err := reg.Route("no-slash")
		if err == nil {
			t.Fatal("expected error for invalid format")
		}
	})
}

func TestRegistryRegisterAndRoute(t *testing.T) {
	reg := NewRegistry()

	// Empty registry should fail
	_, _, err := reg.Route("test/model")
	if err == nil {
		t.Fatal("expected error from empty registry")
	}

	// Register and route
	reg.Register(&mockProvider{name: "test"})
	p, model, err := reg.Route("test/model")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "test" {
		t.Errorf("name = %q", p.Name())
	}
	if model != "model" {
		t.Errorf("model = %q", model)
	}
}

func TestRegistryHasProvider(t *testing.T) {
	reg := NewRegistry()
	if reg.HasProvider("vertex") {
		t.Fatal("expected empty registry to report no vertex provider")
	}

	reg.Register(&mockProvider{name: "vertex"})
	if !reg.HasProvider("vertex") {
		t.Fatal("expected registered vertex provider to be found")
	}

	reg.Unregister("vertex")
	if reg.HasProvider("vertex") {
		t.Fatal("expected unregistered vertex provider to be absent")
	}
}

func TestRouteDefaultAliases(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "kilocode"})
	reg.Register(&mockProvider{name: "openrouter"})

	cases := []struct {
		input        string
		wantProvider string
		wantModel    string
	}{
		{"kilo/anthropic/claude-sonnet-4", "kilocode", "anthropic/claude-sonnet-4"},
		{"or/openai/gpt-4o", "openrouter", "openai/gpt-4o"},
	}
	for _, tc := range cases {
		p, model, err := reg.Route(tc.input)
		if err != nil {
			t.Errorf("Route(%q) error: %v", tc.input, err)
			continue
		}
		if p.Name() != tc.wantProvider {
			t.Errorf("Route(%q) provider = %q, want %q", tc.input, p.Name(), tc.wantProvider)
		}
		if model != tc.wantModel {
			t.Errorf("Route(%q) model = %q, want %q", tc.input, model, tc.wantModel)
		}
	}
}

func TestRouteKnownProviderUnconfiguredErrors(t *testing.T) {
	// anthropic is a known canonical provider name. With no provider
	// registered and no default, this must NOT silently reroute through
	// OpenRouter — it must error.
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "openrouter"})

	_, _, err := reg.Route("anthropic/claude-sonnet-4")
	if err == nil {
		t.Fatal("expected error: known provider unconfigured should not reroute to openrouter")
	}
}

func TestRouteUnknownPrefixErrorsWithoutCatalog(t *testing.T) {
	// "moonshotai" is not a canonical provider. Without a catalog, even with
	// OpenRouter registered, we must not blindly forward — error instead.
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "openrouter"})

	_, _, err := reg.Route("moonshotai/kimi-k2.5")
	if err == nil {
		t.Fatal("expected error: unknown prefix without catalog confirmation must not reach openrouter")
	}
}

func TestRouteZAIDirectNeverImplicitOpenRouter(t *testing.T) {
	// `z-ai/glm-5` must route to the direct Z.AI provider when registered
	// and must NEVER silently divert to OpenRouter just because OR is also
	// registered. The OR path is only taken when the caller writes the
	// explicit `openrouter/z-ai/glm-5` prefix.
	reg := NewRegistry()
	zai := &mockProvider{name: "z-ai"}
	or := &mockProvider{name: "openrouter"}
	reg.Register(zai)
	reg.Register(or)
	// A populated catalog must not influence strict routing.
	reg.SetCatalog(&ModelCatalog{
		models: map[string]ModelInfo{
			"z-ai/glm-5":            {ID: "z-ai/glm-5"},
			"openrouter/z-ai/glm-5": {ID: "openrouter/z-ai/glm-5"},
		},
		source: "static",
	})

	p, model, err := reg.Route("z-ai/glm-5")
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if p.Name() != "z-ai" {
		t.Errorf("provider = %q, want z-ai (direct, never openrouter)", p.Name())
	}
	if model != "glm-5" {
		t.Errorf("model = %q, want glm-5", model)
	}

	// Explicit OR prefix is the only way to route this model via OpenRouter.
	pOR, modelOR, err := reg.Route("openrouter/z-ai/glm-5")
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if pOR.Name() != "openrouter" {
		t.Errorf("explicit openrouter prefix routed to %q, want openrouter", pOR.Name())
	}
	if modelOR != "z-ai/glm-5" {
		t.Errorf("model = %q, want z-ai/glm-5", modelOR)
	}

	// With Z.AI not registered, no implicit OR fallback either — strict error.
	reg2 := NewRegistry()
	reg2.Register(or)
	reg2.SetCatalog(&ModelCatalog{
		models: map[string]ModelInfo{"openrouter/z-ai/glm-5": {ID: "openrouter/z-ai/glm-5"}},
		source: "static",
	})
	if _, _, err := reg2.Route("z-ai/glm-5"); err == nil {
		t.Fatal("expected error when z-ai not registered (no implicit OR fallback allowed)")
	}
}

func TestRouteStrictExplicitOpenRouterPrefix(t *testing.T) {
	// Strict mode: only the explicit prefix routes. No catalog-driven implicit
	// fallback. `anthropic/foo` with only OpenRouter registered must error.
	reg := NewRegistry()
	reg.Register(&mockProvider{name: "openrouter"})

	if _, _, err := reg.Route("anthropic/claude-sonnet-4"); err == nil {
		t.Fatal("expected error: anthropic prefix with no anthropic provider must not implicitly route to openrouter")
	}

	p, model, err := reg.Route("openrouter/anthropic/claude-sonnet-4")
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if p.Name() != "openrouter" {
		t.Errorf("provider = %q, want openrouter", p.Name())
	}
	if model != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q, want anthropic/claude-sonnet-4", model)
	}
}

func TestRouteDefaultProviderCatchAll(t *testing.T) {
	// CLI mode: a default provider acts as catch-all and receives the full
	// qualified model id so it can re-route on the gateway.
	reg := NewRegistry()
	reg.SetDefault(&mockProvider{name: "gateway-proxy"})

	p, model, err := reg.Route("anthropic/claude-sonnet-4")
	if err != nil {
		t.Fatalf("Route error: %v", err)
	}
	if p.Name() != "gateway-proxy" {
		t.Errorf("provider = %q, want gateway-proxy", p.Name())
	}
	if model != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q, want full qualified name", model)
	}
}

func TestMockProviderStream(t *testing.T) {
	p := &mockProvider{name: "test"}
	ch, err := p.StreamCompletion(context.Background(), &CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	ev := <-ch
	if ev.Delta == nil || ev.Delta.Text != "hello" {
		t.Errorf("unexpected event: %+v", ev)
	}
}
