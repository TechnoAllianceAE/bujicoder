package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsForNonPositiveLimits(t *testing.T) {
	tests := []struct {
		name          string
		yaml          string
		wantMaxSteps  int
		wantMaxTokens int
	}{
		{
			name:          "omitted limits",
			yaml:          "id: a\nmodel: m\n",
			wantMaxSteps:  defaultMaxSteps,
			wantMaxTokens: defaultMaxTokens,
		},
		{
			name:          "negative limits are treated as unset",
			yaml:          "id: a\nmodel: m\nmax_steps: -1\nmax_tokens: -20\n",
			wantMaxSteps:  defaultMaxSteps,
			wantMaxTokens: defaultMaxTokens,
		},
		{
			name:          "explicit limits are preserved",
			yaml:          "id: a\nmodel: m\nmax_steps: 3\nmax_tokens: 512\n",
			wantMaxSteps:  3,
			wantMaxTokens: 512,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def, err := LoadBytes([]byte(tt.yaml), "inline")
			if err != nil {
				t.Fatal(err)
			}
			if def.MaxSteps != tt.wantMaxSteps {
				t.Errorf("LoadBytes MaxSteps = %d, want %d", def.MaxSteps, tt.wantMaxSteps)
			}
			if def.MaxTokens != tt.wantMaxTokens {
				t.Errorf("LoadBytes MaxTokens = %d, want %d", def.MaxTokens, tt.wantMaxTokens)
			}

			path := filepath.Join(t.TempDir(), "agent.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			fromFile, err := LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if fromFile.MaxSteps != tt.wantMaxSteps || fromFile.MaxTokens != tt.wantMaxTokens {
				t.Errorf("LoadFile limits = (%d, %d), want (%d, %d)",
					fromFile.MaxSteps, fromFile.MaxTokens, tt.wantMaxSteps, tt.wantMaxTokens)
			}
		})
	}
}
