package cron

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func testScheduler(t *testing.T) *Scheduler {
	t.Helper()
	dir := t.TempDir()
	log := zerolog.New(zerolog.NewTestWriter(t))
	return NewScheduler(dir, log)
}

func TestScheduler_CreateAndList(t *testing.T) {
	s := testScheduler(t)

	job, err := s.Create("test-job", "5m", "echo hello")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if job.Name != "test-job" {
		t.Errorf("Name = %q", job.Name)
	}
	if !job.Enabled {
		t.Error("should be enabled")
	}

	jobs := s.List()
	if len(jobs) != 1 {
		t.Errorf("expected 1 job, got %d", len(jobs))
	}
}

func TestScheduler_Delete(t *testing.T) {
	s := testScheduler(t)

	job, _ := s.Create("to-delete", "10m", "echo bye")
	err := s.Delete(job.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if len(s.List()) != 0 {
		t.Error("expected 0 jobs after delete")
	}

	err = s.Delete("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent job")
	}
}

func TestScheduler_MinInterval(t *testing.T) {
	s := testScheduler(t)

	_, err := s.Create("too-fast", "10s", "echo fast")
	if err == nil {
		t.Error("expected error for interval < 1 minute")
	}
}

func TestScheduler_Persistence(t *testing.T) {
	dir := t.TempDir()
	log := zerolog.New(zerolog.NewTestWriter(t))

	// Create a job
	s1 := NewScheduler(dir, log)
	s1.Create("persistent", "5m", "echo saved")

	// Verify file exists
	fp := filepath.Join(dir, "cron.json")
	if _, err := os.Stat(fp); os.IsNotExist(err) {
		t.Fatal("cron.json not created")
	}

	// Load from disk
	s2 := NewScheduler(dir, log)
	jobs := s2.List()
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job after reload, got %d", len(jobs))
	}
	if jobs[0].Name != "persistent" {
		t.Errorf("Name = %q, want 'persistent'", jobs[0].Name)
	}
}

func TestScheduler_FormatList(t *testing.T) {
	s := testScheduler(t)

	empty := s.FormatList()
	if empty != "No cron jobs scheduled." {
		t.Errorf("unexpected empty format: %q", empty)
	}

	s.Create("test", "5m", "echo hi")
	list := s.FormatList()
	if !containsStr(list, "test") {
		t.Errorf("format missing job name: %s", list)
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"1h", 1 * time.Hour},
		{"24h", 24 * time.Hour},
		{"invalid", 10 * time.Minute}, // default
	}

	for _, tt := range tests {
		got := parseDuration(tt.input)
		if got != tt.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tt.input, got, tt.want)
		}
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
