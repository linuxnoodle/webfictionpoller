package worker

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
)

// Job describes a unit of polling work submitted to the pool.
type Job struct {
	Series   models.Series
	Provider providers.Provider

	// Sources, when non-empty, is the ordered list of alternate hosting
	// locations for the series. processJob tries each in order until one
	// succeeds; failures are recorded via SourceHealthRecorder. When empty,
	// processJob falls back to the single Provider+Series pair above.
	Sources []models.SeriesSource
}

// SourceHealthRecorder lets processJob record per-source success/failure
// without importing handlers. *handlers.Store implements it.
type SourceHealthRecorder interface {
	RecordSourceOK(id int64) error
	RecordSourceFail(id int64, errMsg string) error
	AutoPromoteIfFailing(seriesID int64, threshold int) (int64, error)
}

// ProviderMetrics is the per-provider observability surface exposed via the
// API and the plugins page. All fields are safe for concurrent reads.
type ProviderMetrics struct {
	// LastPollAt is the most recent time a poll completed (success or error).
	LastPollAt atomic.Int64 // unix nano; 0 = never
	// LastErrorAt is the most recent time a poll errored.
	LastErrorAt atomic.Int64
	// LastError holds the most recent error message, if any.
	LastError atomic.Pointer[string]
	// LastChapterCount is the number of chapters the most recent poll returned.
	LastChapterCount atomic.Int64
	// TotalPolls counts completed polls (success or error).
	TotalPolls atomic.Int64
	// TotalErrors counts failed polls.
	TotalErrors atomic.Int64
	// TotalChapters counts chapters discovered across all polls.
	TotalChapters atomic.Int64
}

// MetricsSnapshot is a point-in-time copy of ProviderMetrics, safe to JSON-
// serialize. We snapshot under no lock because the atomics guarantee we read
// a consistent-enough view for display; perfect cross-field consistency is
// not required for monitoring.
type MetricsSnapshot struct {
	Name             string    `json:"name"`
	LastPollAt       time.Time `json:"last_poll_at,omitempty"`
	LastErrorAt      time.Time `json:"last_error_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	LastChapterCount int64     `json:"last_chapter_count"`
	TotalPolls       int64     `json:"total_polls"`
	TotalErrors      int64     `json:"total_errors"`
	TotalChapters    int64     `json:"total_chapters"`
}

// WorkerPool consumes polling Jobs and rate-limits them per provider using
// the rate spec from each provider's plugin.Meta. The pool shares one
// rate limiter and one concurrency semaphore per provider across all worker
// goroutines, so Meta.Rate is honoured globally rather than per-worker.
type WorkerPool struct {
	jobs       chan Job
	providers  map[string]providers.Provider
	mu         sync.Mutex
	wg         sync.WaitGroup
	stopCh     chan struct{}
	onChapters func(seriesID int64, chapters []models.Chapter)

	// sourceHealth, when set, lets processJob record per-source polling
	// outcomes and trigger auto-promotion. Optional; nil disables multi-
	// source failover (legacy single-source path).
	sourceHealth SourceHealthRecorder

	// autoPromoteThreshold is the consecutive-fail count at which the
	// primary source is auto-demoted in favour of the healthiest alternate.
	// 0 disables auto-promotion.
	autoPromoteThreshold int

	// Shared per-provider rate limiters + concurrency semaphores + metrics.
	// Built once at construction from each provider's Meta().Rate.
	limiters   map[string]*rateLimiter
	semaphores map[string]chan struct{}
	metrics    map[string]*ProviderMetrics

	pollMu     sync.Mutex
	pollTotal  int
	pollDone   int
	pollActive bool
}

// rateLimiter is a token-bucket. Tokens refill at `rate` per second up to
// `burst` capacity. wait() blocks until a token is available or stop fires.
type rateLimiter struct {
	tokenCh chan struct{}
	ticker  *time.Ticker
	stopCh  chan struct{}
	stopOnce sync.Once
}

// newRateLimiterFromSpec constructs a token-bucket honoring a RateSpec.
// Defaults to 1 RPS / burst 1 when spec is zero-valued (defensive — every
// registered provider should set a rate, but we don't want to hang if one
// doesn't).
func newRateLimiterFromSpec(spec plugin.RateSpec) *rateLimiter {
	rate := spec.RequestsPerSecond
	if rate <= 0 {
		rate = 1.0
	}
	burst := spec.Burst
	if burst < 1 {
		burst = 1
	}
	period := time.Duration(float64(time.Second) / rate)
	if period < 10*time.Millisecond {
		period = 10 * time.Millisecond // cap absurdly fast rates
	}
	rl := &rateLimiter{
		tokenCh: make(chan struct{}, burst),
		ticker:  time.NewTicker(period),
		stopCh:  make(chan struct{}),
	}
	// Seed with the full burst so the first `burst` requests don't wait.
	for i := 0; i < burst; i++ {
		rl.tokenCh <- struct{}{}
	}
	go rl.run()
	return rl
}

func (rl *rateLimiter) run() {
	for {
		select {
		case <-rl.stopCh:
			return
		case <-rl.ticker.C:
			// Non-blocking refill: drop the token if the bucket is full.
			select {
			case rl.tokenCh <- struct{}{}:
			default:
			}
		}
	}
}

// wait blocks until a token is available or stopCh fires. Returns false if
// stopped before acquiring.
func (rl *rateLimiter) wait(stopCh <-chan struct{}) bool {
	select {
	case <-rl.tokenCh:
		return true
	case <-stopCh:
		return false
	}
}

func (rl *rateLimiter) stop() {
	rl.stopOnce.Do(func() {
		rl.ticker.Stop()
		close(rl.stopCh)
	})
}

// NewWorkerPool constructs a pool with `numWorkers` consumer goroutines. The
// per-provider rate limiters and semaphores are derived from each provider's
// plugin.Meta().Rate; providers that don't implement plugin.Provider fall
// back to the defaults (1 RPS, burst 1, concurrency 1).
func NewWorkerPool(numWorkers int, providerList []providers.Provider, onChapters func(seriesID int64, chapters []models.Chapter)) *WorkerPool {
	wp := &WorkerPool{
		jobs:       make(chan Job, 1000),
		providers:  make(map[string]providers.Provider),
		stopCh:     make(chan struct{}),
		onChapters: onChapters,
		limiters:   make(map[string]*rateLimiter),
		semaphores: make(map[string]chan struct{}),
		metrics:    make(map[string]*ProviderMetrics),
	}

	for _, p := range providerList {
		name := p.Name()
		wp.providers[name] = p

		spec := rateSpecFor(p)
		wp.limiters[name] = newRateLimiterFromSpec(spec)

		conc := spec.Concurrency
		if conc < 1 {
			conc = 1
		}
		wp.semaphores[name] = make(chan struct{}, conc)

		wp.metrics[name] = &ProviderMetrics{}
	}

	for i := 0; i < numWorkers; i++ {
		wp.wg.Add(1)
		go wp.run(i)
	}

	return wp
}

// rateSpecFor reads a provider's RateSpec from its plugin.Meta when available.
func rateSpecFor(p providers.Provider) plugin.RateSpec {
	if pp, ok := p.(plugin.Provider); ok {
		return pp.Meta().Rate
	}
	return plugin.RateSpec{}
}

func (wp *WorkerPool) Submit(job Job) {
	select {
	case wp.jobs <- job:
	default:
		logging.Error("[worker] job queue full, dropping series %d", job.Series.ID)
	}
}

func (wp *WorkerPool) Stop() {
	close(wp.stopCh)
	wp.wg.Wait()
	for _, rl := range wp.limiters {
		rl.stop()
	}
}

// SetSourceHealthRecorder wires the per-source health recorder. Without it,
// multi-source failover is silently disabled (jobs with .Sources still run
// but their per-source outcomes aren't persisted).
func (wp *WorkerPool) SetSourceHealthRecorder(r SourceHealthRecorder) { wp.sourceHealth = r }

// SetAutoPromoteThreshold configures auto-promotion: when a primary source
// exceeds this many consecutive failures, the pool calls AutoPromoteIfFailing
// to swap in the healthiest alternate. Pass 0 to disable (default).
func (wp *WorkerPool) SetAutoPromoteThreshold(n int) { wp.autoPromoteThreshold = n }

func (wp *WorkerPool) run(id int) {
	defer wp.wg.Done()
	for {
		select {
		case <-wp.stopCh:
			return
		case job := <-wp.jobs:
			wp.processJob(id, job)
		}
	}
}

// processJob handles a single polling job: acquire rate-limit token + concurrency
// slot, run the poll, record metrics, dispatch any chapters found.
func (wp *WorkerPool) processJob(workerID int, job Job) {
	defer wp.FinishPoll()

	// Multi-source path: try each source in priority order until one succeeds.
	if len(job.Sources) > 0 {
		wp.processJobMultiSource(workerID, job)
		return
	}

	// Legacy single-source path.
	name := job.Provider.Name()
	rl, ok := wp.limiters[name]
	if !ok {
		logging.Error("[worker %d] no rate limiter for provider %q", workerID, name)
		return
	}
	sem := wp.semaphores[name]
	metrics := wp.metrics[name]

	if !rl.wait(wp.stopCh) {
		return
	}
	select {
	case sem <- struct{}{}:
	case <-wp.stopCh:
		return
	}
	defer func() { <-sem }()

	jitter := time.Duration(200+rand.IntN(800)) * time.Millisecond
	select {
	case <-time.After(jitter):
	case <-wp.stopCh:
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	chapters, err := wp.pollOne(ctx, workerID, job.Provider, job.Series)
	wp.recordProviderOutcome(name, metrics, len(chapters), err)
	if err != nil {
		logging.Error("[worker %d] series %d (%s): poll failed: %v", workerID, job.Series.ID, job.Series.Title, err)
		return
	}
	if len(chapters) > 0 && wp.onChapters != nil {
		wp.onChapters(job.Series.ID, chapters)
	}
	logging.Info("[worker %d] series %q: found %d chapters", workerID, job.Series.Title, len(chapters))
}

// processJobMultiSource iterates job.Sources in order, polling each until one
// succeeds. Per-source outcomes are recorded via the health recorder; an
// auto-promotion check runs at the end.
func (wp *WorkerPool) processJobMultiSource(workerID int, job Job) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var (
		won              bool
		winningChapters  []models.Chapter
		winningProvider  string
		winningSourceID  int64
		lastErr          string
	)

	for _, src := range job.Sources {
		if ctx.Err() != nil {
			break
		}
		p, ok := wp.GetProvider(src.ProviderName)
		if !ok {
			logging.Error("[worker %d] source %d: provider %q not registered", workerID, src.ID, src.ProviderName)
			if wp.sourceHealth != nil {
				_ = wp.sourceHealth.RecordSourceFail(src.ID, "provider not registered")
			}
			continue
		}

		// Acquire the per-provider rate-limit token + concurrency slot.
		rl := wp.limiters[src.ProviderName]
		sem := wp.semaphores[src.ProviderName]
		if rl != nil {
			if !rl.wait(wp.stopCh) {
				return
			}
		}
		if sem != nil {
			select {
			case sem <- struct{}{}:
			case <-wp.stopCh:
				return
			}
		}

		// Construct a series view pointing at this source so the provider's
		// MatchURL/PollUpdates operate on the correct URL.
		srcSeries := job.Series
		srcSeries.SourceURL = src.SourceURL
		srcSeries.ProviderName = src.ProviderName

		chapters, err := wp.pollOne(ctx, workerID, p, srcSeries)
		wp.recordProviderOutcome(src.ProviderName, wp.metrics[src.ProviderName], len(chapters), err)

		if sem != nil {
			<-sem
		}

		if err != nil {
			lastErr = err.Error()
			logging.Error("[worker %d] series %d source %d (%s): poll failed: %v", workerID, job.Series.ID, src.ID, src.ProviderName, err)
			if wp.sourceHealth != nil {
				_ = wp.sourceHealth.RecordSourceFail(src.ID, lastErr)
			}
			continue
		}

		// Success: record + dispatch + bail out (first source wins).
		if wp.sourceHealth != nil {
			_ = wp.sourceHealth.RecordSourceOK(src.ID)
		}
		won = true
		winningChapters = chapters
		winningProvider = src.ProviderName
		winningSourceID = src.ID
		break
	}

	if won {
		if len(winningChapters) > 0 && wp.onChapters != nil {
			wp.onChapters(job.Series.ID, winningChapters)
		}
		logging.Info("[worker %d] series %q: source %d (%s) yielded %d chapters",
			workerID, job.Series.Title, winningSourceID, winningProvider, len(winningChapters))
	} else {
		logging.Error("[worker %d] series %q: all %d sources failed; last error: %s",
			workerID, job.Series.Title, len(job.Sources), lastErr)
	}

	// Auto-promotion check: if the primary is failing hard, swap in the
	// healthiest alternate for next cycle.
	if wp.sourceHealth != nil && wp.autoPromoteThreshold > 0 {
		if promoted, err := wp.sourceHealth.AutoPromoteIfFailing(job.Series.ID, wp.autoPromoteThreshold); err == nil && promoted > 0 {
			logging.Info("[worker %d] series %d: auto-promoted source %d to primary", workerID, job.Series.ID, promoted)
		}
	}
}

// pollOne invokes a single provider's PollUpdates (or PollUpdatesCtx if the
// provider is context-aware). Centralises the ctx-poller type assertion so
// both the legacy and multi-source paths share it.
func (wp *WorkerPool) pollOne(ctx context.Context, workerID int, p providers.Provider, series models.Series) ([]models.Chapter, error) {
	type ctxPoller interface {
		PollUpdatesCtx(ctx context.Context, s models.Series) ([]models.Chapter, error)
	}
	if cp, ok := p.(ctxPoller); ok {
		return cp.PollUpdatesCtx(ctx, series)
	}
	return p.PollUpdates(series)
}

// recordProviderOutcome updates the per-provider metrics atomics after a poll.
func (wp *WorkerPool) recordProviderOutcome(name string, metrics *ProviderMetrics, chapterCount int, err error) {
	if metrics == nil {
		return
	}
	now := time.Now().UnixNano()
	metrics.LastPollAt.Store(now)
	metrics.TotalPolls.Add(1)
	if err != nil {
		metrics.LastErrorAt.Store(now)
		metrics.TotalErrors.Add(1)
		errStr := err.Error()
		metrics.LastError.Store(&errStr)
		return
	}
	metrics.LastChapterCount.Store(int64(chapterCount))
	metrics.TotalChapters.Add(int64(chapterCount))
}

// MetricsSnapshots returns a point-in-time copy of every provider's metrics.
// The returned slice is ordered by provider registration order (via the
// internal map — callers should not rely on a specific order; sort client-side
// if needed).
func (wp *WorkerPool) MetricsSnapshots() []MetricsSnapshot {
	wp.mu.Lock()
	names := make([]string, 0, len(wp.metrics))
	for name := range wp.metrics {
		names = append(names, name)
	}
	wp.mu.Unlock()

	out := make([]MetricsSnapshot, 0, len(names))
	for _, name := range names {
		m := wp.metrics[name]
		snap := MetricsSnapshot{
			Name:             name,
			LastPollAt:       nanoToTime(m.LastPollAt.Load()),
			LastErrorAt:      nanoToTime(m.LastErrorAt.Load()),
			LastChapterCount: m.LastChapterCount.Load(),
			TotalPolls:       m.TotalPolls.Load(),
			TotalErrors:      m.TotalErrors.Load(),
			TotalChapters:    m.TotalChapters.Load(),
		}
		if p := m.LastError.Load(); p != nil {
			snap.LastError = *p
		}
		out = append(out, snap)
	}
	return out
}

func nanoToTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

func (wp *WorkerPool) GetProvider(name string) (providers.Provider, bool) {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	p, ok := wp.providers[name]
	return p, ok
}

func (wp *WorkerPool) AllProviders() map[string]providers.Provider {
	wp.mu.Lock()
	defer wp.mu.Unlock()
	result := make(map[string]providers.Provider, len(wp.providers))
	for k, v := range wp.providers {
		result[k] = v
	}
	return result
}

func (wp *WorkerPool) StartPoll(count int) {
	wp.pollMu.Lock()
	defer wp.pollMu.Unlock()
	wp.pollTotal = count
	wp.pollDone = 0
	wp.pollActive = count > 0
}

func (wp *WorkerPool) FinishPoll() {
	wp.pollMu.Lock()
	defer wp.pollMu.Unlock()
	wp.pollDone++
	if wp.pollDone >= wp.pollTotal {
		wp.pollActive = false
	}
}

type PollStatus struct {
	Active bool `json:"active"`
	Total  int  `json:"total"`
	Done   int  `json:"done"`
}

func (wp *WorkerPool) PollProgress() PollStatus {
	wp.pollMu.Lock()
	defer wp.pollMu.Unlock()
	return PollStatus{
		Active: wp.pollActive,
		Total:  wp.pollTotal,
		Done:   wp.pollDone,
	}
}

// SubmitAllActive is a convenience for the API's /poll/now trigger: it reads
// every active series from the store (via the supplied ActiveSeriesLister)
// and enqueues a job for each. The store interface is kept minimal so the
// worker package doesn't pull in handlers.
type ActiveSeriesLister interface {
	GetAllActiveSeries() ([]models.Series, error)
}

// SubmitAll enqueues every active series for polling using the supplied lister
// (typically *handlers.Store). Returns the count enqueued.
func (wp *WorkerPool) SubmitAll(lister ActiveSeriesLister) (int, error) {
	all, err := lister.GetAllActiveSeries()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, series := range all {
		p, ok := wp.GetProvider(series.ProviderName)
		if !ok {
			continue
		}
		wp.Submit(Job{Series: series, Provider: p})
		count++
	}
	wp.StartPoll(count)
	return count, nil
}
