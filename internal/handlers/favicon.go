package handlers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/models"
)

type FaviconCache struct {
	mu    sync.RWMutex
	icons map[string][]byte
}

func NewFaviconCache() *FaviconCache {
	fc := &FaviconCache{icons: make(map[string][]byte)}
	go fc.prefetch()
	return fc
}

func (fc *FaviconCache) prefetch() {
	client := &http.Client{Timeout: 10 * time.Second}
	for _, name := range models.ProviderNames() {
		url := models.ProviderFaviconSource(name)
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
	name := r.PathValue("provider")
	name = strings.TrimSuffix(name, ".ico")
	fc.mu.RLock()
	data, ok := fc.icons[name]
	fc.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/x-icon")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("ETag", "\""+name+"\"")
	http.ServeContent(w, r, name+".ico", time.Time{}, bytes.NewReader(data))
}
