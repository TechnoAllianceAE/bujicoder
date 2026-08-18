package cron

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// An unparseable schedule must be rejected instead of silently falling back to
// a 10 minute interval the user never asked for.
func TestCreateRejectsUnparseableSchedule(t *testing.T) {
	s := testScheduler(t)

	tests := []struct {
		name     string
		schedule string
		wantErr  bool
	}{
		{name: "valid duration", schedule: "15m"},
		{name: "prose", schedule: "every day", wantErr: true},
		{name: "cron expression", schedule: "*/5 * * * *", wantErr: true},
		{name: "empty", schedule: "", wantErr: true},
		{name: "too short", schedule: "30s", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job, err := s.Create("j-"+tc.name, tc.schedule, "echo hi")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Create(%q) succeeded with job %+v; want an error", tc.schedule, job)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create(%q): %v", tc.schedule, err)
			}
		})
	}
}

// A jobs file whose next_id is stale must not hand out an id that already
// exists, which would silently replace the existing job.
func TestLoadRepairsNextIDSoCreateCannotClobber(t *testing.T) {
	dir := t.TempDir()
	state := `{"jobs":[{"id":"cron_1","name":"first","schedule":"5m","command":"echo 1","enabled":true},
	                   {"id":"cron_2","name":"second","schedule":"5m","command":"echo 2","enabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "cron.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewScheduler(dir, zerolog.New(zerolog.NewTestWriter(t)))
	job, err := s.Create("third", "5m", "echo 3")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.ID == "cron_1" || job.ID == "cron_2" {
		t.Fatalf("Create reused existing id %q", job.ID)
	}
	if got := len(s.List()); got != 3 {
		t.Fatalf("scheduler holds %d jobs, want 3: a job was overwritten", got)
	}
}

// The jobs file must be replaced atomically, never truncated in place.
func TestSaveIsAtomicAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewScheduler(dir, zerolog.New(zerolog.NewTestWriter(t)))
	if _, err := s.Create("j", "5m", "echo hi"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cron.json" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected only cron.json, found %v", names)
	}

	data, err := os.ReadFile(filepath.Join(dir, "cron.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs   []Job `json:"jobs"`
		NextID int   `json:"next_id"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("persisted file is not valid JSON: %v", err)
	}
	if len(parsed.Jobs) != 1 || parsed.NextID != 1 {
		t.Fatalf("unexpected persisted state: %+v", parsed)
	}
}

// A long-running job must not hold the scheduler lock: List/Create/Delete (and
// therefore the TUI) have to stay responsive while a job runs.
func TestCheckAndFireDoesNotHoldTheLockWhileJobsRun(t *testing.T) {
	s := testScheduler(t)
	job, err := s.Create("slow", "1m", "sleep 30")
	if err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	s.jobs[job.ID].NextRun = time.Now().Add(-time.Second)
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.checkAndFire(ctx, time.Now())

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.List()
		_, _ = s.Create("other", "5m", "echo hi")
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler lock was held while a job was running")
	}

	// Cancelling the scheduler context must terminate the in-flight job.
	cancel()
	waited := make(chan struct{})
	go func() { s.running.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("job survived scheduler cancellation")
	}
}

// A due job is rescheduled before it runs, so a slow job is not fired again on
// every subsequent tick.
func TestCheckAndFireClaimsJobsBeforeRunning(t *testing.T) {
	s := testScheduler(t)
	job, err := s.Create("claim", "5m", "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.jobs[job.ID].NextRun = time.Now().Add(-time.Second)
	s.mu.Unlock()

	now := time.Now()
	s.checkAndFire(context.Background(), now)

	s.mu.RLock()
	next := s.jobs[job.ID].NextRun
	s.mu.RUnlock()
	if !next.After(now) {
		t.Fatalf("NextRun = %v was not advanced past %v", next, now)
	}
	s.running.Wait()
}

func TestStartIsIdempotentAndStopWaits(t *testing.T) {
	s := testScheduler(t)
	s.Start()
	s.Start() // must not launch a second loop or leak the first cancel func
	s.Stop()
	s.Stop() // must not block or panic
}

// A job's shell children must be cleaned up, not left as orphans.
func TestExecuteCommandReapsGrandchildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	// bash starts a grandchild that outlives the timeout, records its pid, then
	// blocks so the job hits cancellation.
	cmd := "sleep 300 & echo $! > " + pidFile + "; sleep 300"

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- executeCommand(ctx, cmd) }()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Skipf("shell did not report a grandchild pid: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(10 * time.Second):
		t.Fatal("executeCommand did not return after cancellation")
	}

	deadline = time.Now().Add(5 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // grandchild reaped
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("grandchild %d survived: orphaned cron job process", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestBoundedBufferCapsOutput(t *testing.T) {
	b := &boundedBuffer{limit: 10}
	n, err := b.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("Write = (%d, %v), want (16, nil): a short write would kill the child", n, err)
	}
	if _, err := b.Write([]byte("more")); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	got := b.String()
	if !strings.HasPrefix(got, "0123456789") {
		t.Fatalf("buffer = %q", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("truncation was not reported: %q", got)
	}
	if len(got) > 40 {
		t.Fatalf("buffer grew past its limit: %d bytes", len(got))
	}
}

// Output from a runaway job must not be buffered without bound.
func TestExecuteCommandBoundsJobOutput(t *testing.T) {
	err := executeCommand(context.Background(), "head -c 200000 /dev/zero | tr '\\0' 'a'; exit 1")
	if err == nil {
		t.Fatal("expected the failing command to report an error")
	}
	if len(err.Error()) > maxJobOutput+256 {
		t.Fatalf("error message is %d bytes; job output was not bounded", len(err.Error()))
	}
}

func TestConcurrentPersistIsRaceFree(t *testing.T) {
	s := testScheduler(t)
	if _, err := s.Create("j", "5m", "echo hi"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.persist()
			s.List()
		}()
	}
	wg.Wait()
}
