package cron

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// AgentFunc is a function that sends a prompt to an agent and returns its response.
type AgentFunc func(ctx context.Context, prompt string) (string, error)

// OutputFunc is called with the agent response when a cron job completes.
// It can be used to display results (e.g. print to terminal in chat mode).
type OutputFunc func(jobName, response string)

// Job represents a scheduled task.
type Job struct {
	Name     string
	Schedule string        // cron-like: "30m", "1h", "24h", or time.Duration parseable string
	Prompt   string        // prompt to send to the agent
	Paused   bool          // if true, the job is paused and not running
	AgentFn  AgentFunc
	OutputFn OutputFunc    // optional: called with the response when the job completes
	interval time.Duration // parsed interval
}

// runningJob tracks a job and its cancel function for individual stopping.
type runningJob struct {
	job    Job
	cancel context.CancelFunc
	paused bool
}

// Scheduler runs cron jobs at their configured intervals.
type Scheduler struct {
	mu      sync.Mutex
	jobs    []Job
	running map[string]*runningJob // keyed by job name
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// NewScheduler creates a new cron scheduler.
func NewScheduler() *Scheduler {
	return &Scheduler{
		running: make(map[string]*runningJob),
	}
}

// Add registers a new job with the scheduler.
func (s *Scheduler) Add(job Job) error {
	d, err := time.ParseDuration(job.Schedule)
	if err != nil {
		return err
	}
	job.interval = d

	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, job)
	return nil
}

// Start begins running all scheduled jobs.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	s.ctx = ctx
	s.cancel = cancel

	for _, job := range s.jobs {
		if _, exists := s.running[job.Name]; exists {
			continue // already running
		}
		s.startJobLocked(ctx, job)
	}

	slog.Info("cron scheduler started", "jobs", len(s.running))
}

// startJobLocked starts a single job. Caller must hold s.mu.
func (s *Scheduler) startJobLocked(ctx context.Context, job Job) {
	jobCtx, jobCancel := context.WithCancel(ctx)
	s.running[job.Name] = &runningJob{job: job, cancel: jobCancel}
	s.wg.Add(1)
	go s.runJob(jobCtx, job)
}

// Stop cancels all running jobs and waits for them to finish.
func (s *Scheduler) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	slog.Info("cron scheduler stopped")
}

// Remove stops and removes a job by name. Returns an error if not found.
func (s *Scheduler) Remove(name string) error {
	s.mu.Lock()
	rj, ok := s.running[name]
	if ok {
		rj.cancel()
		delete(s.running, name)
	}
	// Also remove from the jobs slice
	found := false
	for i, j := range s.jobs {
		if j.Name == name {
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			found = true
			break
		}
	}
	s.mu.Unlock()

	if !found && !ok {
		return fmt.Errorf("job %q not found", name)
	}

	slog.Info("cron job removed", "name", name)
	return nil
}

// Pause stops a running job's goroutine but keeps it in the list.
func (s *Scheduler) Pause(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rj, ok := s.running[name]
	if !ok {
		// Check if it exists in jobs at all
		for _, j := range s.jobs {
			if j.Name == name {
				return fmt.Errorf("job %q is not running", name)
			}
		}
		return fmt.Errorf("job %q not found", name)
	}
	if rj.paused {
		return fmt.Errorf("job %q is already paused", name)
	}

	rj.cancel()
	rj.paused = true

	// Update the job in the jobs slice
	for i, j := range s.jobs {
		if j.Name == name {
			s.jobs[i].Paused = true
			break
		}
	}

	slog.Info("cron job paused", "name", name)
	return nil
}

// Resume restarts a paused job.
func (s *Scheduler) Resume(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rj, ok := s.running[name]
	if !ok {
		return fmt.Errorf("job %q not found", name)
	}
	if !rj.paused {
		return fmt.Errorf("job %q is not paused", name)
	}

	if s.ctx == nil {
		return fmt.Errorf("scheduler not started")
	}

	// Restart the goroutine
	jobCtx, jobCancel := context.WithCancel(s.ctx)
	rj.cancel = jobCancel
	rj.paused = false
	s.wg.Add(1)
	go s.runJob(jobCtx, rj.job)

	// Update the job in the jobs slice
	for i, j := range s.jobs {
		if j.Name == name {
			s.jobs[i].Paused = false
			break
		}
	}

	slog.Info("cron job resumed", "name", name)
	return nil
}

// UpdateSchedule stops a running job, updates its schedule, and restarts it.
func (s *Scheduler) UpdateSchedule(name, newSchedule string) error {
	d, err := time.ParseDuration(newSchedule)
	if err != nil {
		return fmt.Errorf("invalid schedule %q: %w", newSchedule, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rj, ok := s.running[name]
	if !ok {
		return fmt.Errorf("job %q not found", name)
	}

	// Cancel current goroutine (if running)
	wasPaused := rj.paused
	if !wasPaused {
		rj.cancel()
	}

	// Update the job
	rj.job.Schedule = newSchedule
	rj.job.interval = d

	// Update in jobs slice
	for i, j := range s.jobs {
		if j.Name == name {
			s.jobs[i].Schedule = newSchedule
			s.jobs[i].interval = d
			break
		}
	}

	// Restart if it wasn't paused
	if !wasPaused && s.ctx != nil {
		jobCtx, jobCancel := context.WithCancel(s.ctx)
		rj.cancel = jobCancel
		s.wg.Add(1)
		go s.runJob(jobCtx, rj.job)
	}

	slog.Info("cron job schedule updated", "name", name, "schedule", newSchedule)
	return nil
}

func (s *Scheduler) runJob(ctx context.Context, job Job) {
	defer s.wg.Done()

	ticker := time.NewTicker(job.interval)
	defer ticker.Stop()

	slog.Info("cron job registered", "name", job.Name, "interval", job.interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			slog.Info("cron job running", "name", job.Name)

			response, err := job.AgentFn(ctx, job.Prompt)
			if err != nil {
				if ctx.Err() != nil {
					return // context cancelled, stop gracefully
				}
				slog.Error("cron job failed", "name", job.Name, "error", err)
				continue
			}

			slog.Info("cron job completed", "name", job.Name, "response_length", len(response))
			if job.OutputFn != nil {
				job.OutputFn(job.Name, response)
			}
		}
	}
}

// Jobs returns the list of configured jobs.
func (s *Scheduler) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Job, len(s.jobs))
	copy(result, s.jobs)

	// Sync paused state from running map
	for i, j := range result {
		if rj, ok := s.running[j.Name]; ok {
			result[i].Paused = rj.paused
		}
	}
	return result
}
