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
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
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
	done     chan struct{}
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
func (s *Scheduler) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel

	go s.runLoop(ctx)
	s.log.Info().Int("jobs", len(s.jobs)).Msg("cron scheduler started")
}

// Stop signals the scheduler to shut down and waits for completion.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
	}
}

// Create adds a new job and persists to disk.
func (s *Scheduler) Create(name, schedule, command string) (*Job, error) {
	interval := parseDuration(schedule)
	if interval < 1*time.Minute {
		return nil, fmt.Errorf("minimum schedule interval is 1 minute, got %v", interval)
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
			s.checkAndFire(now)
		}
	}
}

func (s *Scheduler) checkAndFire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, job := range s.jobs {
		if !job.Enabled || now.Before(job.NextRun) {
			continue
		}

		// Fire the job
		s.log.Info().Str("id", job.ID).Str("name", job.Name).Msg("firing cron job")
		if err := executeCommand(job.Command); err != nil {
			job.LastErr = err.Error()
			s.log.Error().Str("id", job.ID).Err(err).Msg("cron job failed")
		} else {
			job.LastErr = ""
		}

		job.LastRun = now
		interval := parseDuration(job.Schedule)
		job.NextRun = now.Add(interval)
	}

	s.save()
}

func executeCommand(command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", command)
	} else {
		cmd = exec.Command("bash", "-c", command)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Persistence

func (s *Scheduler) load() {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return
	}

	var state struct {
		Jobs   []*Job `json:"jobs"`
		NextID int    `json:"next_id"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return
	}

	for _, j := range state.Jobs {
		s.jobs[j.ID] = j
	}
	s.nextID = state.NextID
}

func (s *Scheduler) save() {
	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}

	state := struct {
		Jobs   []*Job `json:"jobs"`
		NextID int    `json:"next_id"`
	}{
		Jobs:   jobs,
		NextID: s.nextID,
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(s.filePath)
	os.MkdirAll(dir, 0755)
	os.WriteFile(s.filePath, data, 0644)
}

// parseDuration parses a schedule string. Supports Go durations (5m, 1h, 24h).
func parseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 10 * time.Minute // default
	}
	return d
}
