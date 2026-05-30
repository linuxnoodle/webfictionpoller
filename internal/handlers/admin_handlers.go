package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/linuxnoodle/webfictionpoller/internal/logging"
)

func (h *Handler) LogsPage(w http.ResponseWriter, r *http.Request) {
	renderTemplate(w, r, "logs", nil)
}

func (h *Handler) LogsData(w http.ResponseWriter, r *http.Request) {
	lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
	if lines <= 0 {
		lines = 500
	}

	logs, err := logging.ReadLogs(h.logDir, lines)
	if err != nil {
		internalError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"lines": logs,
		"total": len(logs),
	})
}

func (h *Handler) ProviderConfigPage(w http.ResponseWriter, r *http.Request) {
	providers := h.pool.AllProviders()
	configs := make(map[string]string)
	usernames := make(map[string]string)
	loginTested := make(map[string]bool)
	for name := range providers {
		pc, _ := h.store.GetProviderConfig(name)
		if pc != nil {
			configs[name] = pc.CookieData
			usernames[name] = pc.Username
			loginTested[name] = pc.LoginTested
		}
	}
	renderTemplate(w, r, "provider_config", map[string]interface{}{
		"Providers":   providers,
		"Configs":     configs,
		"Usernames":   usernames,
		"LoginTested": loginTested,
	})
}

func (h *Handler) SaveProviderConfig(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("provider_name")
	cookieData := r.FormValue("cookie_data")
	username := r.FormValue("username")
	password := r.FormValue("password")
	if name == "" {
		http.Error(w, "provider_name required", http.StatusBadRequest)
		return
	}

	var encryptedPassword string
	if h.vault != nil && password != "" {
		enc, err := h.vault.Encrypt(password)
		if err != nil {
			logging.Error("[handler] encrypting password for %s: %v", name, err)
			http.Error(w, "encryption error", http.StatusInternalServerError)
			return
		}
		encryptedPassword = enc
	} else if h.vault != nil && password == "" {
		existing, _ := h.store.GetProviderConfig(name)
		if existing != nil {
			encryptedPassword = existing.EncryptedPassword
		}
	}

	if err := h.store.UpsertProviderConfig(name, cookieData, username, encryptedPassword); err != nil {
		internalError(w, r, err)
		return
	}

	p, ok := h.pool.GetProvider(name)
	if !ok {
		logging.Info("[handler] updated provider config for %s", name)
		w.Header().Set("HX-Redirect", "/admin/providers")
		w.WriteHeader(http.StatusOK)
		return
	}

	if username != "" && encryptedPassword != "" && p.SupportsLogin() && h.vault != nil {
		plainPass, err := h.vault.Decrypt(encryptedPassword)
		if err != nil {
			logging.Error("[handler] decrypting password for %s: %v", name, err)
		} else if err := p.Login(username, plainPass); err != nil {
			logging.Error("[handler] login failed for %s: %v", name, err)
		}
	}

	// Always apply cookies if they exist, either as a primary auth mechanism or a fallback for a failed/blocked login
	if p.RequiresAuth() && cookieData != "" {
		_ = p.SetCookies(cookieData)
	}

	logging.Info("[handler] updated provider config for %s", name)
	w.Header().Set("HX-Redirect", "/admin/providers")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) CheckAuthProvider(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("provider_name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "provider_name required"})
		return
	}

	p, ok := h.pool.GetProvider(name)
	if !ok || !p.SupportsLogin() {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "provider does not support login"})
		return
	}

	pc, err := h.store.GetProviderConfig(name)
	if err != nil || pc == nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "no credentials saved"})
		return
	}

	if pc.Username == "" || pc.EncryptedPassword == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "username and password required"})
		return
	}

	if h.vault == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "encryption not available"})
		return
	}

	plainPass, err := h.vault.Decrypt(pc.EncryptedPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"success": false, "error": "failed to decrypt password"})
		return
	}

	if err := p.Login(pc.Username, plainPass); err != nil {
		_ = h.store.SetLoginTested(name, false)
		writeJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Login failed: invalid credentials"})
		return
	}

	_ = h.store.SetLoginTested(name, true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true})
}

func (h *Handler) GetProviderPassword(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("provider")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "provider required"})
		return
	}

	pc, err := h.store.GetProviderConfig(name)
	if err != nil || pc == nil {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "no config found"})
		return
	}

	if pc.EncryptedPassword == "" {
		writeJSON(w, http.StatusOK, map[string]interface{}{"password": ""})
		return
	}

	if h.vault == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "encryption not available"})
		return
	}

	plainPass, err := h.vault.Decrypt(pc.EncryptedPassword)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "failed to decrypt"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"password": plainPass})
}
