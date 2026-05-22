package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
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

	uc.mu.RLock()
	targetCommit := uc.latestCommit
	uc.mu.RUnlock()

	if targetCommit != "" {
		uc.appendLog("Checking if image for commit " + targetCommit[:7] + " exists on registry...")
		imageTag := "ghcr.io/linuxnoodle/webfictionpoller:sha-" + targetCommit[:7]
		manifestOut, manifestErr := exec.Command("docker", "manifest", "inspect", imageTag).CombinedOutput()
		if manifestErr != nil {
			uc.appendLog("WARNING: Image " + imageTag + " not found on registry yet.")
			uc.appendLog("  The CI build may not have completed. Check: https://github.com/linuxnoodle/webfictionpoller/actions")
			uc.appendLog("  Trying pull anyway in case latest tag is ahead...")
			uc.appendLog(string(manifestOut))
		} else {
			uc.appendLog("Image " + imageTag + " found on registry.")
		}
	}

	uc.appendLog("Pulling latest image...")
	pullCmd := exec.Command("docker", "compose", "-f", composeFile, "pull", "app")
	pullOut, pullErr := pullCmd.CombinedOutput()
	uc.appendLog(string(pullOut))
	if pullErr != nil {
		uc.appendLog("ERROR: pull failed: " + pullErr.Error())
		return
	}

	if !strings.Contains(string(pullOut), "Downloaded newer image") && !strings.Contains(string(pullOut), "Pulled") {
		uc.appendLog("Image is already up to date. No restart needed.")
		return
	}

	uc.appendLog("Restarting via helper container...")

	hostname, _ := os.Hostname()
	imgOut, _ := exec.Command("docker", "inspect", hostname, "--format", "{{.Config.Image}}").Output()
	image := strings.TrimSpace(string(imgOut))
	if image == "" {
		image = "docker:cli"
	}

	script := fmt.Sprintf(
		"sleep 2 && docker compose -f %s up -d --force-recreate --remove-orphans",
		composeFile,
	)

	cmd := exec.Command("docker", "run", "--rm", "-d",
		"--name", "wfp-updater",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", projectDir+":"+projectDir+":ro",
		image,
		"sh", "-c", script,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		uc.appendLog("Helper container failed: " + strings.TrimSpace(string(out)) + " (" + err.Error() + ")")
		uc.appendLog("Attempting direct restart (container may stay down)...")
		upCmd := exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--force-recreate", "--remove-orphans")
		upCmd.CombinedOutput()
		return
	}

	uc.appendLog("Update scheduled. Container is restarting...")
	uc.appendLog("This page will auto-reconnect once the new container is ready.")
	logging.Info("self-update: restart scheduled via helper container")
}
