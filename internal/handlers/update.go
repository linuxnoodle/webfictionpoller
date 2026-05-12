package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
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
	renderTemplate(w, r, "version", map[string]interface{}{
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

	projectDir := os.Getenv("COMPOSE_PROJECT_DIR")
	if projectDir == "" {
		uc.appendLog("ERROR: COMPOSE_PROJECT_DIR not set")
		uc.appendLog("  Mount /var/run/docker.sock and set COMPOSE_PROJECT_DIR=/opt/webfictionpoller")
		return
	}

	composeFile := projectDir + "/docker-compose.yml"

	uc.appendLog("Pulling latest images...")
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull")
	pullOut, pullErr := pullCmd.CombinedOutput()
	uc.appendLog(string(pullOut))
	if pullErr != nil {
		uc.appendLog("ERROR: pull failed: " + pullErr.Error())
		return
	}

	uc.appendLog("Restarting with new images...")
	upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--remove-orphans")
	upOut, upErr := upCmd.CombinedOutput()
	uc.appendLog(string(upOut))
	if upErr != nil {
		uc.appendLog("ERROR: restart failed: " + upErr.Error())
		return
	}

	uc.appendLog("Update complete! Container is restarting...")
	uc.appendLog("This page will auto-reconnect once the new container is ready.")
	logging.Info("self-update: completed successfully")
}
