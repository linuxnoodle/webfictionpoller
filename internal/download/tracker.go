// Package download tracks the progress of asynchronous, user-triggered
// downloads (comic chapter pages, generated EPUB/CBZ bundles, batch archives).
//
// The tracker is in-memory: progress is lost on restart. That's acceptable
// because downloads are short-lived and idempotent — callers can re-request
// a download and the tracker repopulates from whatever bytes already landed
// in the blob store.
package download

import (
	"context"
	"sync"
	"time"
)

// State enumerates where a job is in its lifecycle.
type State string

const (
	StateRunning   State = "running"
	StateComplete  State = "complete"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

// Job is a unit of tracked work. A Job's key is a string the caller chooses
// (e.g. "comic-chapter:42"); only one job per key runs at a time. Resubmitting
// a running job returns the existing tracker.
type Job struct {
	Key string

	mu        sync.Mutex
	state     State
	total     int
	done      int
	err       string
	startedAt time.Time
	endedAt   time.Time

	// cancel lets the tracker stop a running job. Set when Start is called
	// with a non-trivial context cancel.
	cancel context.CancelFunc
}

// Status is a snapshot of a Job safe to serialize. Callers should never
// mutate Job directly; use the methods on Tracker.
type Status struct {
	Key       string    `json:"key"`
	State     State     `json:"state"`
	Total     int       `json:"total"`
	Done      int       `json:"done"`
	Percent   int       `json:"percent"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
}

// Tracker is the process-global job registry. Construct once, share freely.
// The zero value is unusable; use NewTracker.
type Tracker struct {
	mu   sync.Mutex
	jobs map[string]*Job
}

func NewTracker() *Tracker { return &Tracker{jobs: make(map[string]*Job)} }

// Start registers a job under key and invokes fn. If a job with the same key
// is already running, Start returns the existing job's status without invoking
// fn. If the most recent job for key is terminal (complete/failed/cancelled),
// it is replaced.
//
// fn receives a context that is cancelled if Cancel(key) is called, and a
// Reporter for incrementing Done. fn's error return value is recorded on the
// job; returning nil marks the job complete.
func (t *Tracker) Start(key string, total int, fn func(ctx context.Context, rep Reporter) error) Status {
	t.mu.Lock()
	if existing, ok := t.jobs[key]; ok {
		existing.mu.Lock()
		running := existing.state == StateRunning
		existing.mu.Unlock()
		if running {
			t.mu.Unlock()
			return existing.snapshot()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &Job{
		Key:       key,
		state:     StateRunning,
		total:     total,
		startedAt: time.Now(),
		cancel:    cancel,
	}
	t.jobs[key] = j
	t.mu.Unlock()

	go func() {
		defer cancel()
		err := fn(ctx, Reporter{job: j})
		j.mu.Lock()
		j.endedAt = time.Now()
		if err != nil {
			if ctx.Err() != nil {
				j.state = StateCancelled
			} else {
				j.state = StateFailed
				j.err = err.Error()
			}
		} else {
			j.state = StateComplete
		}
		j.mu.Unlock()
	}()

	return j.snapshot()
}

// Reporter lets a running job advance its progress without holding the job
// lock. Constructed by Tracker.Start; the type is exported only so function
// signatures can name it.
type Reporter struct{ job *Job }

// SetTotal adjusts the total. Useful when the real page count is only known
// after the first upstream call.
func (r Reporter) SetTotal(n int) {
	r.job.mu.Lock()
	r.job.total = n
	r.job.mu.Unlock()
}

// Inc marks one additional unit done.
func (r Reporter) Inc() { r.Add(1) }

// Add marks n additional units done.
func (r Reporter) Add(n int) {
	r.job.mu.Lock()
	r.job.done += n
	r.job.mu.Unlock()
}

// Done returns the current done count. For display inside long-running jobs.
func (r Reporter) Done() int {
	r.job.mu.Lock()
	defer r.job.mu.Unlock()
	return r.job.done
}

// ContextErr is a convenience for checking cancellation inside a job.
func (r Reporter) ContextErr(ctx context.Context) error { return ctx.Err() }

// Status returns a snapshot of the job for key, or ok=false if no such job.
func (t *Tracker) Status(key string) (Status, bool) {
	t.mu.Lock()
	j, ok := t.jobs[key]
	t.mu.Unlock()
	if !ok {
		return Status{}, false
	}
	return j.snapshot(), true
}

// Cancel signals the job for key to stop. No-op if the job is missing or
// already terminal.
func (t *Tracker) Cancel(key string) {
	t.mu.Lock()
	j, ok := t.jobs[key]
	t.mu.Unlock()
	if !ok {
		return
	}
	j.mu.Lock()
	if j.state == StateRunning && j.cancel != nil {
		j.cancel()
	}
	j.mu.Unlock()
}

// Forget removes a terminal job from the tracker. No-op on running jobs.
func (t *Tracker) Forget(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	j, ok := t.jobs[key]
	if !ok {
		return
	}
	j.mu.Lock()
	terminal := j.state != StateRunning
	j.mu.Unlock()
	if terminal {
		delete(t.jobs, key)
	}
}

func (j *Job) snapshot() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	percent := 0
	if j.total > 0 {
		percent = (j.done * 100) / j.total
		if percent > 100 {
			percent = 100
		}
	} else if j.state == StateComplete {
		percent = 100
	}
	return Status{
		Key:       j.Key,
		State:     j.state,
		Total:     j.total,
		Done:      j.done,
		Percent:   percent,
		Error:     j.err,
		StartedAt: j.startedAt,
		EndedAt:   j.endedAt,
	}
}
