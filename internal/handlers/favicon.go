package handlers

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

type FaviconCache struct {
	mu     sync.RWMutex
	icons  map[string][]byte
	misses map[string]struct{} // providers whose upstream failed; don't retry forever
	fetch  sync.Map            // in-flight singleflight: name -> chan struct{}
}

func NewFaviconCache() *FaviconCache {
	fc := &FaviconCache{icons: make(map[string][]byte)}
	// Lazy: favicons are fetched on first request rather than at startup,
	// so a slow upstream or a missing site no longer delays boot.
	return fc
}

// fetchAndCache retrieves the favicon for `name` from its upstream source URL
// (plugin.Meta.FaviconURL) and caches the bytes. Best-effort: errors are
// logged and the absence is recorded via fc.misses so we don't hammer a
// broken upstream on every request.
func (fc *FaviconCache) fetchAndCache(name string) ([]byte, bool) {
	sourceURL := plugin.Default.FaviconSourceURL(name)
	if sourceURL == "" {
		return nil, false
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(sourceURL)
	if err != nil {
		logging.Error("[favicon] fetch %s: %v", name, err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.Error("[favicon] %s upstream status %d", name, resp.StatusCode)
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if err != nil {
		logging.Error("[favicon] read %s: %v", name, err)
		return nil, false
	}
	fc.mu.Lock()
	fc.icons[name] = data
	fc.mu.Unlock()
	logging.Info("[favicon] cached %s (%d bytes)", name, len(data))
	return data, true
}

func (fc *FaviconCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("provider")
	name := strings.TrimSuffix(raw, ".ico")

	// Fast path: cached.
	fc.mu.RLock()
	data, ok := fc.icons[name]
	_, knownMiss := fc.misses[name]
	fc.mu.RUnlock()
	if ok {
		fc.writeIcon(w, r, name, data)
		return
	}
	if knownMiss {
		// Upstream previously failed; don't hammer it. 404 quickly.
		http.NotFound(w, r)
		return
	}

	// First miss for this name since boot: one goroutine wins the race and
	// fetches while others wait on the per-name channel. Subsequent requests
	// for the same name see fetch.Load missing (already deleted) and take
	// the slow path again, but will hit either the cache or the miss set.
	chNew := make(chan struct{})
	chIface, loaded := fc.fetch.LoadOrStore(name, chNew)
	ch := chIface.(chan struct{})
	if !loaded {
		// We won; do the fetch in this goroutine so the caller waits.
		fc.fetchLocked(name)
	} else {
		// Someone else is fetching; wait briefly. On timeout we 404 so the
		// client isn't blocked indefinitely on a slow upstream.
		select {
		case <-ch:
		case <-time.After(8 * time.Second):
			http.NotFound(w, r)
			return
		}
	}

	fc.mu.RLock()
	data, ok = fc.icons[name]
	_, knownMiss = fc.misses[name]
	fc.mu.RUnlock()
	if !ok || knownMiss {
		http.NotFound(w, r)
		return
	}
	fc.writeIcon(w, r, name, data)
}

// fetchLocked is run by whichever goroutine won the single-flight race. It
// hits upstream, records the result (cache or miss), and closes the channel
// to release waiters.
func (fc *FaviconCache) fetchLocked(name string) {
	defer func() {
		if chIface, ok := fc.fetch.Load(name); ok {
			close(chIface.(chan struct{}))
			fc.fetch.Delete(name)
		}
	}()
	if _, ok := fc.fetchAndCache(name); !ok {
		fc.mu.Lock()
		if fc.misses == nil {
			fc.misses = make(map[string]struct{})
		}
		fc.misses[name] = struct{}{}
		fc.mu.Unlock()
	}
}

func (fc *FaviconCache) writeIcon(w http.ResponseWriter, r *http.Request, name string, data []byte) {
	ct := http.DetectContentType(data)
	if ct == "application/octet-stream" {
		ct = "image/x-icon"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", "\""+name+"\"")
	http.ServeContent(w, r, name+".ico", time.Time{}, bytes.NewReader(data))
}
