package handlers

import (
	"encoding/json"
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

	composeFile := ""
	candidates := []string{
		"/opt/webfictionpoller/docker-compose.yml",
		"/opt/webfictionpoller/docker-compose.yaml",
		"/app/docker-compose.yml",
		"/app/docker-compose.yaml",
	}
	for _, candidate := range candidates {
		uc.appendLog("  checking " + candidate)
		if _, err := os.Stat(candidate); err == nil {
			composeFile = candidate
			break
		} else {
			uc.appendLog("  not found: " + err.Error())
		}
	}
	if composeFile == "" {
		wd, _ := os.Getwd()
		uc.appendLog("ERROR: docker-compose.yml not found")
		uc.appendLog("  working dir: " + wd)
		uc.appendLog("  env COMPOSE_FILE: " + os.Getenv("COMPOSE_FILE"))
		de, _ := os.ReadDir("/")
		var entries []string
		for _, e := range de {
			entries = append(entries, e.Name())
		}
		uc.appendLog("  / contents: " + strings.Join(entries, ", "))
		logging.Error("self-update: docker-compose.yml not found")
		return
	}
	uc.appendLog("  found: " + composeFile)

	installDir := strings.TrimSuffix(composeFile, "/docker-compose.yml")
	installDir = strings.TrimSuffix(installDir, "/docker-compose.yaml")
	uc.appendLog("  install dir: " + installDir)

	if _, err := exec.LookPath("git"); err != nil {
		uc.appendLog("ERROR: git not found in PATH")
		logging.Error("self-update: git not found: %v", err)
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		uc.appendLog("ERROR: docker not found in PATH")
		logging.Error("self-update: docker not found: %v", err)
		return
	}

	steps := []struct {
		name string
		cmd  string
		args []string
		dir  string
	}{
		{"git pull", "git", []string{"pull", "-q"}, installDir},
		{"get commit", "git", []string{"rev-parse", "HEAD"}, installDir},
		{"docker compose build", "docker", []string{"compose", "-f", composeFile, "build", "app"}, ""},
		{"docker compose up", "docker", []string{"compose", "-f", composeFile, "up", "-d", "--remove-orphans"}, ""},
	}

	var commit string
	for _, step := range steps {
		uc.appendLog("> " + step.name)
		logging.Info("self-update: %s", step.name)

		cmd := exec.Command(step.cmd, step.args...)
		if step.dir != "" {
			cmd.Dir = step.dir
		}
		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			if step.name == "get commit" {
				commit = strings.TrimSpace(string(out))
			}
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

	if commit != "" {
		uc.appendLog("> docker compose rebuild with commit " + commit[:7])
		cmd := exec.Command("docker", "compose", "-f", composeFile, "build", "--build-arg", "VERSION_COMMIT="+commit, "app")
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
			logging.Error("self-update failed at rebuild: %v", err)
			return
		}

		cmd = exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--remove-orphans")
		out, err = cmd.CombinedOutput()
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
			logging.Error("self-update failed at up: %v", err)
			return
		}
	}

	uc.appendLog("Update complete!")
	logging.Info("self-update completed successfully")
}
