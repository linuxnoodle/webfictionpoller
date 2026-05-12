package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/version"
)

type UpdateChecker struct {
	mu           sync.RWMutex
	lastCheck    time.Time
	latestCommit string
	checkErr     error
	updating     bool
	updateLog    string
}

func NewUpdateChecker() *UpdateChecker {
	uc := &UpdateChecker{}
	go uc.check()
	return uc
}

type githubCommitResp struct {
	SHA string `json:"sha"`
}

type VersionResponse struct {
	CurrentCommit string `json:"current_commit"`
	CurrentShort  string `json:"current_short"`
	LatestCommit  string `json:"latest_commit,omitempty"`
	LatestShort   string `json:"latest_short,omitempty"`
	UpdateAvail   bool   `json:"update_available"`
	Error         string `json:"error,omitempty"`
	LastChecked   string `json:"last_checked,omitempty"`
	Updating      bool   `json:"updating,omitempty"`
	UpdateLog     string `json:"update_log,omitempty"`
}

func (uc *UpdateChecker) check() {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/linuxnoodle/webfictionpoller/commits/master")
	if err != nil {
		uc.mu.Lock()
		uc.checkErr = err
		uc.lastCheck = time.Now()
		uc.mu.Unlock()
		return
	}
	defer resp.Body.Close()

	var body githubCommitResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		uc.mu.Lock()
		uc.checkErr = err
		uc.lastCheck = time.Now()
		uc.mu.Unlock()
		return
	}

	uc.mu.Lock()
	uc.latestCommit = body.SHA
	uc.checkErr = nil
	uc.lastCheck = time.Now()
	uc.mu.Unlock()
}

func (uc *UpdateChecker) ShouldCheck() bool {
	uc.mu.RLock()
	defer uc.mu.RUnlock()
	return time.Since(uc.lastCheck) > 1*time.Hour
}

func (uc *UpdateChecker) GetStatus() VersionResponse {
	uc.mu.RLock()
	defer uc.mu.RUnlock()

	current := version.Commit()
	resp := VersionResponse{
		CurrentCommit: current,
		CurrentShort:  version.Short(),
		LastChecked:   uc.lastCheck.Format(time.RFC3339),
	}

	if uc.checkErr != nil {
		resp.Error = uc.checkErr.Error()
		return resp
	}

	if uc.latestCommit != "" {
		resp.LatestCommit = uc.latestCommit
		if len(uc.latestCommit) > 7 {
			resp.LatestShort = uc.latestCommit[:7]
		} else {
			resp.LatestShort = uc.latestCommit
		}
		resp.UpdateAvail = current != uc.latestCommit && current != "dev"
	}

	resp.Updating = uc.updating
	resp.UpdateLog = uc.updateLog

	return resp
}

func (h *Handler) VersionAPI(w http.ResponseWriter, r *http.Request) {
	if h.updateChecker.ShouldCheck() {
		go h.updateChecker.check()
	}

	resp := h.updateChecker.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) VersionCheckNow(w http.ResponseWriter, r *http.Request) {
	h.updateChecker.check()
	resp := h.updateChecker.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) VersionPage(w http.ResponseWriter, r *http.Request) {
	status := h.updateChecker.GetStatus()
	renderTemplate(w, "version", map[string]interface{}{
		"Version": status,
	})
}

func (h *Handler) SelfUpdate(w http.ResponseWriter, r *http.Request) {
	uc := h.updateChecker
	uc.mu.Lock()
	if uc.updating {
		uc.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "update already in progress"})
		return
	}
	uc.updating = true
	uc.updateLog = ""
	uc.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "started"})

	go h.runWatchtowerUpdate()
}

func (uc *UpdateChecker) appendLog(line string) {
	uc.mu.Lock()
	uc.updateLog += line + "\n"
	uc.mu.Unlock()
	logging.Info("self-update: %s", line)
}

func (h *Handler) runWatchtowerUpdate() {
	uc := h.updateChecker
	defer func() {
		uc.mu.Lock()
		uc.updating = false
		uc.mu.Unlock()
	}()

	watchtowerURL := os.Getenv("WATCHTOWER_URL")
	if watchtowerURL == "" {
		watchtowerURL = "http://watchtower:8080"
	}

	token := os.Getenv("WATCHTOWER_TOKEN")

	uc.appendLog("Triggering Watchtower update...")
	uc.appendLog("  target: " + watchtowerURL)

	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequest("POST", watchtowerURL+"/v1/update", nil)
	if err != nil {
		uc.appendLog("ERROR: failed to create request: " + err.Error())
		return
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	uc.appendLog("  sending update request...")
	resp, err := client.Do(req)
	if err != nil {
		uc.appendLog("ERROR: Watchtower request failed: " + err.Error())
		uc.appendLog("")
		uc.appendLog("Possible fixes:")
		uc.appendLog("  1. Upgrade Docker in the LXC: curl -fsSL https://get.docker.com | sh")
		uc.appendLog("     (Watchtower requires Docker 20.10+, you may have an older version)")
		uc.appendLog("  2. Restart Watchtower: docker compose up -d watchtower")
		uc.appendLog("  3. Update manually from the LXC shell:")
		uc.appendLog("     docker compose -f /opt/webfictionpoller/docker-compose.yml pull && docker compose -f /opt/webfictionpoller/docker-compose.yml up -d")
		logging.Error("self-update: watchtower request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		uc.appendLog("Watchtower triggered successfully!")
		uc.appendLog("The container will restart with the new image shortly.")
		uc.appendLog("This page will auto-reconnect once the new container is ready.")
		logging.Info("self-update: watchtower triggered successfully")
	} else {
		uc.appendLog(fmt.Sprintf("ERROR: Watchtower returned HTTP %d", resp.StatusCode))
		uc.appendLog("  hint: check WATCHTOWER_TOKEN matches in both containers")
		logging.Error("self-update: watchtower returned HTTP %d", resp.StatusCode)
	}
}
