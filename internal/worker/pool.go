package worker

import (
	"math/rand/v2"
	"sync"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
)

type Job struct {
	Series   models.Series
	Provider providers.Provider
}

type WorkerPool struct {
	jobs       chan Job
	providers  map[string]providers.Provider
	mu         sync.Mutex
	wg         sync.WaitGroup
	stopCh     chan struct{}
	onChapters func(seriesID int64, chapters []models.Chapter)

	pollMu      sync.Mutex
	pollTotal   int
	pollDone    int
	pollActive  bool
}

type rateLimiter struct {
	ticker   *time.Ticker
	tokenCh  chan struct{}
	stopOnce sync.Once
	stopCh   chan struct{}
}

func newRateLimiter() *rateLimiter {
	rl := &rateLimiter{
		ticker:  time.NewTicker(1 * time.Second),
		tokenCh: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
	}
	rl.tokenCh <- struct{}{}
	go rl.run()
	return rl
}

func (rl *rateLimiter) run() {
	for {
		select {
		case <-rl.stopCh:
			return
		case <-rl.ticker.C:
			select {
			case rl.tokenCh <- struct{}{}:
			default:
			}
		}
	}
}

func (rl *rateLimiter) wait(stopCh chan struct{}) bool {
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

func NewWorkerPool(numWorkers int, providerList []providers.Provider, onChapters func(seriesID int64, chapters []models.Chapter)) *WorkerPool {
	wp := &WorkerPool{
		jobs:       make(chan Job, 1000),
		providers:  make(map[string]providers.Provider),
		stopCh:     make(chan struct{}),
		onChapters: onChapters,
	}

	for _, p := range providerList {
		wp.providers[p.Name()] = p
	}

	for i := 0; i < numWorkers; i++ {
		wp.wg.Add(1)
		go wp.run(i)
	}

	return wp
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
}

func (wp *WorkerPool) run(id int) {
	defer wp.wg.Done()
	limiters := make(map[string]*rateLimiter)
	for name := range wp.providers {
		limiters[name] = newRateLimiter()
	}
	defer func() {
		for _, rl := range limiters {
			rl.stop()
		}
	}()

	for {
		select {
		case <-wp.stopCh:
			return
		case job := <-wp.jobs:
			rl, ok := limiters[job.Provider.Name()]
			if !ok {
				continue
			}
			if !rl.wait(wp.stopCh) {
				return
			}

			jitter := time.Duration(500+rand.IntN(1500)) * time.Millisecond
			time.Sleep(jitter)

			chapters, err := job.Provider.PollUpdates(job.Series)
			if err != nil {
				logging.Error("[worker %d] error polling series %d (%s): %v", id, job.Series.ID, job.Series.Title, err)
				wp.FinishPoll()
				continue
			}

			if len(chapters) > 0 && wp.onChapters != nil {
				wp.onChapters(job.Series.ID, chapters)
			}

			wp.FinishPoll()
			logging.Info("[worker %d] series %q: found %d chapters", id, job.Series.Title, len(chapters))
		}
	}
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
