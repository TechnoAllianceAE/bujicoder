// Package cron implements a persistent job scheduler. Jobs are stored on disk
// and executed by a background goroutine that checks for due jobs every 30 seconds.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	// jobTimeout bounds a single job's shell command so a hung job cannot
	// accumulate forever.
	jobTimeout = 5 * time.Minute

	// minInterval is the shortest schedule accepted by Create.
	minInterval = 1 * time.Minute

	// waitDelay caps how long cmd.Wait tolerates a descendant that inherited the
	// job's output pipe after the job itself was killed.
	waitDelay = 5 * time.Second
)

// Job represents a scheduled job.
type Job struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Schedule string    `json:"schedule"` // Go duration (e.g. "5m", "1h") or simple interval
	Command  string    `json:"command"`
	Enabled  bool      `json:"enabled"`
	LastRun  time.Time `json:"last_run,omitempty"`
	NextRun  time.Time `json:"next_run"`
	LastErr  string    `json:"last_error,omitempty"`
}

// Scheduler manages cron jobs with persistence and a background execution loop.
type Scheduler struct {
	mu       sync.RWMutex
	jobs     map[string]*Job
	nextID   int
	filePath string // persistence path (e.g. ~/.bujicoder/cron.json)
	log      zerolog.Logger
	cancel   context.CancelFunc
	started  bool
	done     chan struct{}
	running  sync.WaitGroup // in-flight job commands
}

// NewScheduler creates a scheduler that persists jobs to the given file path.
// Call Start() to begin the background scheduler goroutine.
func NewScheduler(configDir string, log zerolog.Logger) *Scheduler {
	s := &Scheduler{
		jobs:     make(map[string]*Job),
		filePath: filepath.Join(configDir, "cron.json"),
		log:      log.With().Str("component", "cron").Logger(),
		done:     make(chan struct{}),
	}
	s.load()
	return s
}

// Start begins the background scheduler goroutine that checks for due jobs.
// Calling it more than once is a no-op: a second scheduler goroutine would fire
// every job twice and leak when only the newest cancel func is retained.
func (s *Scheduler) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	jobs := len(s.jobs)
	s.mu.Unlock()

	go s.runLoop(ctx)
	s.log.Info().Int("jobs", jobs).Msg("cron scheduler started")
}

// Stop signals the scheduler to shut down and waits for the loop and any
// in-flight job commands to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	started := s.started
	s.started = false
	s.mu.Unlock()

	if !started || cancel == nil {
		return
	}
	cancel()
	<-s.done
	s.running.Wait()
}

// Create adds a new job and persists to disk. The schedule must be a Go
// duration of at least one minute.
func (s *Scheduler) Create(name, schedule, command string) (*Job, error) {
	// An unparseable schedule silently fell back to 10m, so a job created with
	// e.g. "every day" ran every ten minutes instead.
	interval, err := time.ParseDuration(schedule)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule %q: expected a Go duration such as 15m, 1h or 24h", schedule)
	}
	if interval < minInterval {
		return nil, fmt.Errorf("minimum schedule interval is %v, got %v", minInterval, interval)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	id := fmt.Sprintf("cron_%d", s.nextID)

	job := &Job{
		ID:       id,
		Name:     name,
		Schedule: schedule,
		Command:  command,
		Enabled:  true,
		NextRun:  time.Now().Add(interval),
	}
	s.jobs[id] = job
	s.save()

	s.log.Info().Str("id", id).Str("name", name).Str("schedule", schedule).Msg("cron job created")
	return job, nil
}

// Delete removes a job by ID.
func (s *Scheduler) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job not found: %s", id)
	}
	delete(s.jobs, id)
	s.save()

	s.log.Info().Str("id", id).Msg("cron job deleted")
	return nil
}

// List returns all jobs.
func (s *Scheduler) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		result = append(result, j)
	}
	return result
}

// FormatList returns a human-readable display.
func (s *Scheduler) FormatList() string {
	jobs := s.List()
	if len(jobs) == 0 {
		return "No cron jobs scheduled."
	}

	var sb strings.Builder
	sb.WriteString("Scheduled Jobs:\n")
	for _, j := range jobs {
		status := "enabled"
		if !j.Enabled {
			status = "disabled"
		}
		nextIn := time.Until(j.NextRun).Round(time.Second)
		fmt.Fprintf(&sb, "  %s: %s [%s] schedule=%s next=%v cmd=%s\n",
			j.ID, j.Name, status, j.Schedule, nextIn, j.Command)
		if j.LastErr != "" {
			fmt.Fprintf(&sb, "    last error: %s\n", j.LastErr)
		}
	}
	return sb.String()
}

// runLoop is the background goroutine that fires due jobs.
func (s *Scheduler) runLoop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.checkAndFire(ctx, now)
		}
	}
}

// checkAndFire claims every due job under the lock, then runs the commands
// outside it. Holding the lock across command execution would block List,
// Create and Delete — i.e. the whole TUI — for as long as a job runs.
func (s *Scheduler) checkAndFire(ctx context.Context, now time.Time) {
	type due struct {
		id      string
		name    string
		command string
	}

	var claimed []due
	s.mu.Lock()
	for _, job := range s.jobs {
		if !job.Enabled || now.Before(job.NextRun) {
			continue
		}
		// Reschedule immediately so the job is not fired again by the next tick
		// while this run is still in flight.
		job.LastRun = now
		job.NextRun = now.Add(parseDuration(job.Schedule))
		claimed = append(claimed, due{id: job.ID, name: job.Name, command: job.Command})
	}
	s.mu.Unlock()

	if len(claimed) == 0 {
		return
	}
	s.persist()

	for _, job := range claimed {
		s.log.Info().Str("id", job.id).Str("name", job.name).Msg("firing cron job")
		s.running.Add(1)
		go func(job due) {
			defer s.running.Done()
			err := executeCommand(ctx, job.command)
			s.mu.Lock()
			if j, ok := s.jobs[job.id]; ok {
				if err != nil {
					j.LastErr = err.Error()
				} else {
					j.LastErr = ""
				}
			}
			s.mu.Unlock()
			if err != nil {
				s.log.Error().Str("id", job.id).Err(err).Msg("cron job failed")
			}
			s.persist()
		}(job)
	}
}

// executeCommand runs a job's shell command, bounded by jobTimeout and by the
// scheduler's lifetime: cancelling ctx (Stop) terminates a running job.
func executeCommand(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, jobTimeout)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd.exe", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-c", command)
	}
	// The shell may spawn children; put it in its own process group so the whole
	// group can be cleaned up instead of outliving the timeout as orphans.
	setProcessGroup(cmd)
	// Killing only the shell is not enough: descendants keep the output pipe
	// open, so cmd.Wait would block until they exit on their own. Kill the group
	// on cancellation and cap how long Wait tolerates a lingering descendant.
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = waitDelay

	// Capture output through a bounded buffer: a chatty job (e.g. `cat
	// /dev/urandom`) would otherwise buffer unbounded data in memory.
	var out boundedBuffer
	out.limit = maxJobOutput
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()
	killProcessGroup(cmd)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// maxJobOutput caps how much of a job's output is retained for the error message.
const maxJobOutput = 8 << 10

// boundedBuffer collects at most limit bytes and reports every write as
// successful, so the child is never killed by a short write.
type boundedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		if len(p) <= room {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:room]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	if b.truncated {
		return string(b.buf) + " ... (output truncated)"
	}
	return string(b.buf)
}

// Persistence

func (s *Scheduler) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			s.log.Warn().Err(err).Str("path", s.filePath).Msg("cannot read cron jobs")
		}
		return
	}

	var state struct {
		Jobs   []*Job `json:"jobs"`
		NextID int    `json:"next_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		s.log.Warn().Err(err).Str("path", s.filePath).Msg("cron jobs file is malformed; ignoring it")
		return
	}

	for _, j := range state.Jobs {
		if j == nil || j.ID == "" {
			continue
		}
		s.jobs[j.ID] = j
	}
	s.nextID = state.NextID
	// Repair a stale/missing next_id: reusing an id would silently replace an
	// existing job on the next Create.
	for id := range s.jobs {
		var n int
		if _, err := fmt.Sscanf(id, "cron_%d", &n); err == nil && n > s.nextID {
			s.nextID = n
		}
	}
}

// save writes the current state to disk. Callers must hold s.mu.
func (s *Scheduler) save() {
	s.writeState(s.marshalState())
}

// persist writes the current state to disk without the caller holding s.mu.
func (s *Scheduler) persist() {
	s.mu.RLock()
	data := s.marshalState()
	s.mu.RUnlock()
	s.writeState(data)
}

// marshalState serialises the job list. Callers must hold s.mu (read or write).
func (s *Scheduler) marshalState() []byte {
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	// Stable order so the file does not churn between saves.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].ID < jobs[j].ID })

	state := struct {
		Jobs   []*Job `json:"jobs"`
		NextID int    `json:"next_id"`
	}{
		Jobs:   jobs,
		NextID: s.nextID,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		s.log.Error().Err(err).Msg("cannot encode cron jobs")
		return nil
	}
	return data
}

// writeState replaces the jobs file atomically: a crash or full disk mid-write
// would otherwise leave a truncated file and lose every scheduled job.
func (s *Scheduler) writeState(data []byte) {
	if data == nil {
		return
	}
	dir := filepath.Dir(s.filePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		s.log.Error().Err(err).Str("dir", dir).Msg("cannot create cron config dir")
		return
	}
	tmp, err := os.CreateTemp(dir, ".cron-*.json")
	if err != nil {
		s.log.Error().Err(err).Msg("cannot create temp file for cron jobs")
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		s.log.Error().Err(err).Msg("cannot write cron jobs")
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		s.log.Error().Err(err).Msg("cannot close cron jobs temp file")
		return
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		s.log.Warn().Err(err).Msg("cannot chmod cron jobs temp file")
	}
	if err := os.Rename(tmpName, s.filePath); err != nil {
		_ = os.Remove(tmpName)
		s.log.Error().Err(err).Str("path", s.filePath).Msg("cannot replace cron jobs file")
	}
}

// parseDuration parses a schedule string. Supports Go durations (5m, 1h, 24h).
func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 10 * time.Minute // default
	}
	return d
}
