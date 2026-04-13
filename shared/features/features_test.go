package features

import (
	"os"
	"testing"
)

func TestDefaultRegistry_HasFlags(t *testing.T) {
	r := DefaultRegistry()
	flags := r.List()
	if len(flags) < 20 {
		t.Errorf("expected at least 20 flags, got %d", len(flags))
	}
}

func TestRegistry_EnableDisable(t *testing.T) {
	r := DefaultRegistry()

	if r.IsEnabled("GUI_MODE") {
		t.Error("GUI_MODE should be disabled by default")
	}

	r.Enable("GUI_MODE")
	if !r.IsEnabled("GUI_MODE") {
		t.Error("GUI_MODE should be enabled after Enable()")
	}

	r.Disable("GUI_MODE")
	if r.IsEnabled("GUI_MODE") {
		t.Error("GUI_MODE should be disabled after Disable()")
	}
}

func TestRegistry_Toggle(t *testing.T) {
	r := DefaultRegistry()

	enabled := r.Toggle("ULTRAPLAN")
	if !enabled {
		t.Error("Toggle should return true (was off)")
	}
	if !r.IsEnabled("ULTRAPLAN") {
		t.Error("ULTRAPLAN should be enabled after toggle")
	}

	enabled = r.Toggle("ULTRAPLAN")
	if enabled {
		t.Error("Toggle should return false (was on)")
	}
}

func TestRegistry_EnvOverride(t *testing.T) {
	r := DefaultRegistry()

	os.Setenv("BUJI_FEATURE_VOICE_MODE", "true")
	defer os.Unsetenv("BUJI_FEATURE_VOICE_MODE")

	if !r.IsEnabled("VOICE_MODE") {
		t.Error("env override should enable VOICE_MODE")
	}

	// Even if registry says disabled, env wins
	r.Disable("VOICE_MODE")
	if !r.IsEnabled("VOICE_MODE") {
		t.Error("env should override registry disable")
	}
}

func TestRegistry_UnknownFlag(t *testing.T) {
	r := DefaultRegistry()
	if r.IsEnabled("NONEXISTENT_FLAG") {
		t.Error("unknown flag should be disabled")
	}
}

func TestRegistry_ListSorted(t *testing.T) {
	r := DefaultRegistry()
	flags := r.List()

	// Verify sorted by category
	for i := 1; i < len(flags); i++ {
		if flags[i].Category < flags[i-1].Category {
			t.Errorf("flags not sorted by category: %s < %s",
				flags[i].Category, flags[i-1].Category)
		}
	}
}

func TestRegistry_FormatList(t *testing.T) {
	r := DefaultRegistry()
	list := r.FormatList()
	if list == "" {
		t.Error("format should not be empty")
	}
	if !containsStr(list, "AGENT") {
		t.Error("should contain AGENT category")
	}
	if !containsStr(list, "BUJI_FEATURE_") {
		t.Error("should contain env var hint")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
