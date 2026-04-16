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
