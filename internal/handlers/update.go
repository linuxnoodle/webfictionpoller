package handlers

import (
	"bufio"
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
		if _, err := os.Stat(candidate); err == nil {
			composeFile = candidate
			break
		}
	}
	if composeFile == "" {
		wd, _ := os.Getwd()
		uc.appendLog("ERROR: docker-compose.yml not found")
		uc.appendLog("  working dir: " + wd)
		uc.appendLog("  hint: ensure docker-compose.yml is mounted into the container")
		logging.Error("self-update: docker-compose.yml not found")
		return
	}

	installDir := strings.TrimSuffix(composeFile, "/docker-compose.yml")
	installDir = strings.TrimSuffix(installDir, "/docker-compose.yaml")

	if _, err := exec.LookPath("git"); err != nil {
		uc.appendLog("ERROR: git not installed in container")
		uc.appendLog("  hint: add 'git' to Dockerfile apk add")
		logging.Error("self-update: git not found: %v", err)
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		uc.appendLog("ERROR: docker CLI not installed in container")
		uc.appendLog("  hint: add 'docker-cli' to Dockerfile apk add and mount /var/run/docker.sock")
		logging.Error("self-update: docker not found: %v", err)
		return
	}

	uc.appendLog("[1/4] Fetching latest code...")
	logging.Info("self-update: git pull")

	cmd := exec.Command("git", "pull")
	cmd.Dir = installDir
	if err := uc.streamCmd(cmd); err != nil {
		uc.appendLog("ERROR: git pull failed: " + err.Error())
		logging.Error("self-update: git pull failed: %v", err)
		return
	}

	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		uc.appendLog("ERROR: could not determine commit hash")
		logging.Error("self-update: git rev-parse failed: %v", err)
		return
	}
	commit := strings.TrimSpace(string(out))
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	uc.appendLog("  commit: " + short)
	logging.Info("self-update: building commit %s", short)

	uc.appendLog("[2/4] Building Docker image (this may take a few minutes)...")
	logging.Info("self-update: docker compose build")

	cmd = exec.Command("docker", "compose", "-f", composeFile, "build", "--build-arg", "VERSION_COMMIT="+commit, "app")
	if err := uc.streamCmd(cmd); err != nil {
		uc.appendLog("ERROR: docker build failed: " + err.Error())
		logging.Error("self-update: docker build failed: %v", err)
		return
	}

	uc.appendLog("[3/4] Restarting container...")
	logging.Info("self-update: docker compose up")

	cmd = exec.Command("docker", "compose", "-f", composeFile, "up", "-d", "--remove-orphans")
	if err := uc.streamCmd(cmd); err != nil {
		uc.appendLog("ERROR: docker up failed: " + err.Error())
		logging.Error("self-update: docker up failed: %v", err)
		return
	}

	uc.appendLog("[4/4] Cleaning up old images...")
	exec.Command("docker", "image", "prune", "-f").Run()

	uc.appendLog("Update complete! The page will reconnect once the new container is ready.")
	logging.Info("self-update completed successfully")
}

func (uc *UpdateChecker) streamCmd(cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if line != "" {
			uc.appendLog("  " + line)
		}
	}

	scErr := bufio.NewScanner(stderr)
	for scErr.Scan() {
		line := scErr.Text()
		if line != "" && !strings.Contains(line, "CACHED") && !strings.Contains(line, "transferring") {
			uc.appendLog("  " + line)
		}
	}

	return cmd.Wait()
}
