package handlers

import (
	"encoding/json"
	"net/http"
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
	CurrentCommit  string `json:"current_commit"`
	CurrentShort   string `json:"current_short"`
	LatestCommit   string `json:"latest_commit,omitempty"`
	LatestShort    string `json:"latest_short,omitempty"`
	UpdateAvail    bool   `json:"update_available"`
	Error          string `json:"error,omitempty"`
	LastChecked    string `json:"last_checked,omitempty"`
	Updating       bool   `json:"updating,omitempty"`
	UpdateLog      string `json:"update_log,omitempty"`
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
	h.updateChecker.mu.Lock()
	if h.updateChecker.updating {
		h.updateChecker.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "update already in progress"})
		return
	}
	h.updateChecker.updating = true
	h.updateChecker.updateLog = ""
	h.updateChecker.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "started"})

	go h.runSelfUpdate()
}

func (uc *UpdateChecker) appendLog(line string) {
	uc.mu.Lock()
	uc.updateLog += line + "\n"
	uc.mu.Unlock()
}

func (h *Handler) runSelfUpdate() {
	uc := h.updateChecker
	defer func() {
		uc.mu.Lock()
		uc.updating = false
		uc.mu.Unlock()
	}()

	uc.appendLog("Starting self-update...")

	// Find the install dir by looking for docker-compose.yml
	composeFile := ""
	for _, candidate := range []string{
		"/opt/webfictionpoller/docker-compose.yml",
		"/app/docker-compose.yml",
	} {
		if _, err := exec.Command("test", "-f", candidate).CombinedOutput(); err == nil {
			composeFile = candidate
			break
		}
	}
	if composeFile == "" {
		uc.appendLog("ERROR: docker-compose.yml not found")
		logging.Error("self-update: docker-compose.yml not found")
		return
	}

	installDir := strings.TrimSuffix(composeFile, "/docker-compose.yml")
	srcDir := installDir + "/src"

	steps := []struct {
		name string
		cmd  string
		args []string
	}{
		{"git pull", "git", []string{"pull", "-q"}},
		{"docker compose build", "docker", []string{"compose", "-f", composeFile, "build", "--build-arg", "VERSION_COMMIT=$(git rev-parse HEAD)", "app"}},
		{"docker compose up", "docker", []string{"compose", "-f", composeFile, "up", "-d", "--remove-orphans"}},
	}

	for _, step := range steps {
		uc.appendLog("> " + step.name)
		logging.Info("self-update: %s", step.name)

		var cmd *exec.Cmd
		if step.name == "git pull" {
			cmd = exec.Command(step.cmd, step.args...)
			cmd.Dir = srcDir
		} else if strings.Contains(step.name, "build") {
			args := []string{"compose", "-f", composeFile, "build"}
			out, err := exec.Command("git", []string{"-C", srcDir, "rev-parse", "HEAD"}...).Output()
			if err != nil {
				uc.appendLog("ERROR: failed to get git commit: " + err.Error())
				return
			}
			commit := strings.TrimSpace(string(out))
			args = append(args, "--build-arg", "VERSION_COMMIT="+commit, "app")
			cmd = exec.Command(step.cmd, args...)
		} else {
			cmd = exec.Command(step.cmd, step.args...)
		}

		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			for _, l := range lines {
				if l != "" {
					uc.appendLog("  " + l)
				}
			}
		}
		if err != nil {
			uc.appendLog("ERROR: " + err.Error())
			logging.Error("self-update failed at '%s': %v", step.name, err)
			return
		}
	}

	uc.appendLog("Update complete!")
	logging.Info("self-update completed successfully")
}
