package handlers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
)

type FaviconCache struct {
	mu    sync.RWMutex
	icons map[string][]byte
}

func NewFaviconCache() *FaviconCache {
	fc := &FaviconCache{icons: make(map[string][]byte)}
	fc.prefetch()
	return fc
}

func (fc *FaviconCache) prefetch() {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, name := range plugin.Default.Names() {
		url := plugin.Default.FaviconSourceURL(name)
		if url == "" {
			continue
		}
		resp, err := client.Get(url)
		if err != nil {
			log.Printf("[favicon] failed to fetch %s: %v", name, err)
			continue
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 65536))
		resp.Body.Close()
		if err != nil {
			log.Printf("[favicon] failed to read %s: %v", name, err)
			continue
		}
		fc.mu.Lock()
		fc.icons[name] = data
		fc.mu.Unlock()
		log.Printf("[favicon] cached %s (%d bytes)", name, len(data))
	}
}

func (fc *FaviconCache) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("provider")
	name := strings.TrimSuffix(raw, ".ico")
	log.Printf("[favicon] request path=%s raw=%q name=%q", r.URL.Path, raw, name)
	fc.mu.RLock()
	data, ok := fc.icons[name]
	if !ok {
		data, ok = fc.icons[raw]
	}
	var cached []string
	if !ok {
		for k := range fc.icons {
			cached = append(cached, k)
		}
	}
	fc.mu.RUnlock()
	if !ok {
		log.Printf("[favicon] not found: %q (cached keys: %v)", name, cached)
		http.NotFound(w, r)
		return
	}
	ct := http.DetectContentType(data)
	if ct == "application/octet-stream" {
		ct = "image/x-icon"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", "\""+name+"\"")
	http.ServeContent(w, r, name+".ico", time.Time{}, bytes.NewReader(data))
	log.Printf("[favicon] served %s (%d bytes, %s)", name, len(data), ct)
}
