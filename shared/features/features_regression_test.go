package features

import (
	"sync"
	"testing"
)

// List must hand out snapshots: returning the registry's own *Flag pointers
// races with Enable/Disable/Toggle writing Flag.Enabled.
func TestListSnapshotDoesNotRaceWithToggle(t *testing.T) {
	r := DefaultRegistry()

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				r.Toggle("GUI_MODE")
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				for _, f := range r.List() {
					_ = f.Enabled
					_ = f.Name
				}
				_ = r.FormatList()
			}
		}()
	}
	wg.Wait()
}

// Mutating a snapshot must not corrupt registry state.
func TestListSnapshotIsDetached(t *testing.T) {
	r := DefaultRegistry()
	for _, f := range r.List() {
		if f.Name == "GUI_MODE" {
			f.Enabled = true
			f.Description = "hijacked"
		}
	}
	if r.IsEnabled("GUI_MODE") {
		t.Fatal("mutating the value returned by List changed registry state")
	}
	for _, f := range r.List() {
		if f.Name == "GUI_MODE" && f.Description == "hijacked" {
			t.Fatal("registry description was mutated through List")
		}
	}
}

func TestIsEnabledEnvOverride(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "false", value: "false", want: false},
		{name: "garbage is not enabled", value: "yes-please", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := DefaultRegistry()
			t.Setenv("BUJI_FEATURE_GUI_MODE", tc.value)
			if got := r.IsEnabled("GUI_MODE"); got != tc.want {
				t.Fatalf("IsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnknownFlagIsDisabled(t *testing.T) {
	r := DefaultRegistry()
	if r.IsEnabled("NO_SUCH_FLAG") {
		t.Fatal("unknown flag reported as enabled")
	}
	r.Enable("NO_SUCH_FLAG")
	if r.IsEnabled("NO_SUCH_FLAG") {
		t.Fatal("Enable created an unknown flag")
	}
	if r.Toggle("NO_SUCH_FLAG") {
		t.Fatal("Toggle reported an unknown flag as enabled")
	}
}
