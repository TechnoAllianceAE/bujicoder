package app

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func setupModel(step int) Model {
	return Model{state: StateSetup, setupStep: step, historyIdx: -1}
}

// Bracketed paste delivers the whole API key in one KeyMsg. The wizard used to
// accept only single-character input, so a pasted key was silently dropped and
// first-run setup could not be completed without typing the key by hand.
func TestSetupAcceptsPastedAPIKey(t *testing.T) {
	const key = "sk-or-v1-0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{name: "paste", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key), Paste: true}},
		{name: "multi rune runes message", msg: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := setupModel(setupStepQuickKey)
			got, _ := m.handleSetupKeys(tc.msg)
			if got.setupAPIKey != key {
				t.Fatalf("setupAPIKey = %q, want %q", got.setupAPIKey, key)
			}
		})
	}
}

func TestSetupPasteStripsNewlines(t *testing.T) {
	m := setupModel(setupStepQuickKey)
	got, _ := m.handleSetupKeys(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("sk-line1\r\nsk-line2\n"),
		Paste: true,
	})
	if strings.ContainsAny(got.setupAPIKey, "\r\n") {
		t.Fatalf("pasted key kept line breaks: %q", got.setupAPIKey)
	}
	if got.setupAPIKey != "sk-line1sk-line2" {
		t.Fatalf("setupAPIKey = %q", got.setupAPIKey)
	}
}

func TestSetupKeyEntryEditing(t *testing.T) {
	m := setupModel(setupStepQuickKey)
	for _, ch := range []string{"a", "b", "é"} {
		m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(ch)})
	}
	if m.setupAPIKey != "abé" {
		t.Fatalf("setupAPIKey = %q, want abé", m.setupAPIKey)
	}

	// Backspace must remove one rune, not one byte.
	m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.setupAPIKey != "ab" {
		t.Fatalf("setupAPIKey = %q, want ab", m.setupAPIKey)
	}

	// Enter with an empty key must not advance.
	empty := setupModel(setupStepQuickKey)
	got, cmd := empty.handleSetupKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if got.setupStep != setupStepQuickKey || cmd != nil {
		t.Errorf("empty key should not submit: step=%d cmd=%v", got.setupStep, cmd)
	}

	// Backspace on an empty key goes back a step.
	got, _ = empty.handleSetupKeys(tea.KeyMsg{Type: tea.KeyBackspace})
	if got.setupStep != setupStepModeSelect {
		t.Errorf("setupStep = %d, want mode select", got.setupStep)
	}
}

func TestSetupNavigationStaysInRange(t *testing.T) {
	keys := []tea.KeyMsg{
		{Type: tea.KeyUp}, {Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyDown},
		{Type: tea.KeyDown}, {Type: tea.KeyUp},
	}

	t.Run("mode select", func(t *testing.T) {
		m := setupModel(setupStepModeSelect)
		for _, k := range keys {
			m, _ = m.handleSetupKeys(k)
			if m.setupMode < 0 || m.setupMode > 1 {
				t.Fatalf("setupMode = %d out of range", m.setupMode)
			}
			_ = m.renderSetupView()
		}
	})

	t.Run("provider select", func(t *testing.T) {
		m := setupModel(setupStepAdvProvider)
		for range len(setupProviders) + 3 {
			m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyDown})
			if m.setupProvider < 0 || m.setupProvider >= len(setupProviders) {
				t.Fatalf("setupProvider = %d out of range", m.setupProvider)
			}
			_ = m.renderSetupView()
		}
		for range len(setupProviders) + 3 {
			m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyUp})
			if m.setupProvider < 0 || m.setupProvider >= len(setupProviders) {
				t.Fatalf("setupProvider = %d out of range", m.setupProvider)
			}
		}
	})
}

func TestSetupModelSelectionWithNoModels(t *testing.T) {
	m := setupModel(setupStepAdvModels)
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyUp}, {Type: tea.KeyDown}, {Type: tea.KeyEnter}, {Type: tea.KeyBackspace},
	} {
		m, _ = m.handleSetupKeys(k)
	}
	if m.setupModelIdx != 0 {
		t.Errorf("setupModelIdx = %d, want 0 with no models", m.setupModelIdx)
	}
}

func TestSetupModelSelectionWalksAllSlots(t *testing.T) {
	m := setupModel(setupStepAdvModels)
	m.setupModels = []string{"a/one", "b/two", "c/three"}
	m.height = 0 // zero-size terminal must not break scroll math

	// 9 selections (3 modes x 3 roles); the 9th completes setup.
	for i := range 8 {
		m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyDown})
		m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyEnter})
		if m.setupModeStep < 0 || m.setupModeStep > 2 {
			t.Fatalf("step %d: setupModeStep = %d out of range", i, m.setupModeStep)
		}
		if m.setupRoleStep < 0 || m.setupRoleStep > 2 {
			t.Fatalf("step %d: setupRoleStep = %d out of range", i, m.setupRoleStep)
		}
		if m.setupModelIdx < 0 || m.setupModelIdx >= len(m.setupModels) {
			t.Fatalf("step %d: setupModelIdx = %d out of range", i, m.setupModelIdx)
		}
		// Rendering reads setupModeNames[setupModeStep] / setupRoleDescs[setupRoleStep].
		_ = m.renderSetupView()
	}

	// Walking backwards must also stay in range.
	for range 12 {
		m, _ = m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyBackspace})
		if m.setupModeStep < 0 || m.setupModeStep > 2 || m.setupRoleStep < 0 || m.setupRoleStep > 2 {
			t.Fatalf("out of range going back: mode=%d role=%d", m.setupModeStep, m.setupRoleStep)
		}
	}
}

func TestSetupFetchErrorRetryAndSkipKeys(t *testing.T) {
	m := setupModel(setupStepAdvFetching)
	m.setupFetchErr = "invalid API key (HTTP 401)"
	m.setupAPIKey = "sk-bad"

	retry, cmd := m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if retry.setupFetchErr != "" || !retry.setupFetching || cmd == nil {
		t.Errorf("enter should retry the fetch: err=%q fetching=%v cmd=%v",
			retry.setupFetchErr, retry.setupFetching, cmd)
	}

	back, _ := m.handleSetupKeys(tea.KeyMsg{Type: tea.KeyBackspace})
	if back.setupStep != setupStepAdvKey || back.setupFetching {
		t.Errorf("backspace should return to key entry: step=%d fetching=%v", back.setupStep, back.setupFetching)
	}

	_ = m.renderSetupView()
}

func TestHandleModelsFetchedResetsSelection(t *testing.T) {
	m := setupModel(setupStepAdvFetching)
	m.setupFetching = true
	m.setupModelIdx = 42
	m.setupScrollOff = 20
	m.setupSelections[1][1] = "stale/model"

	got, _ := m.handleModelsFetched(modelsFetchedMsg{models: []string{"a/one", "b/two"}})
	if got.setupFetching {
		t.Error("fetching flag should be cleared")
	}
	if got.setupStep != setupStepAdvModels {
		t.Errorf("setupStep = %d, want model selection", got.setupStep)
	}
	// A stale index would index past the new, shorter model list.
	if got.setupModelIdx >= len(got.setupModels) {
		t.Errorf("setupModelIdx = %d out of range for %d models", got.setupModelIdx, len(got.setupModels))
	}
	if got.setupSelections[1][1] != "" {
		t.Error("previous selections should be reset for the new model list")
	}

	failed, _ := m.handleModelsFetched(modelsFetchedMsg{err: errFetch})
	if failed.setupFetchErr == "" || failed.setupFetching {
		t.Errorf("fetch failure must be surfaced: err=%q fetching=%v", failed.setupFetchErr, failed.setupFetching)
	}
}

var errFetch = &fetchError{}

type fetchError struct{}

func (*fetchError) Error() string { return "boom" }

func TestFetchProviderModelsUnknownProvider(t *testing.T) {
	if _, err := fetchProviderModels(context.Background(), "nope", "key"); err == nil {
		t.Fatal("unknown provider must return an error instead of issuing a request")
	}
}
