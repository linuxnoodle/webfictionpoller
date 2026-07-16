package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/microcosm-cc/bluemonday"

	"github.com/linuxnoodle/webfictionpoller/internal/api"
	"github.com/linuxnoodle/webfictionpoller/internal/crypto"
	"github.com/linuxnoodle/webfictionpoller/internal/download"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/opds"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

var contentPolicy = bluemonday.UGCPolicy()

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	logging.Error("[handler] %s %s: %v", r.Method, r.URL.Path, err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

type Handler struct {
	store         *Store
	pool          *worker.WorkerPool
	logDir        string
	updateChecker *UpdateChecker
	vault         *crypto.Vault
	opdsCatalog   *opds.Catalog
	archiver      *worker.Archiver
	downloads     *download.Tracker
	tokens        *api.TokenStore

	// userIDOf resolves the authenticated user ID from the browser session.
	// Injected by main.go to avoid handlers depending on the scs package.
	userIDOf      func(*http.Request) (int64, bool)
}

func NewHandler(store *Store, pool *worker.WorkerPool, logDir string, vault *crypto.Vault) *Handler {
	return &Handler{
		store:         store,
		pool:          pool,
		logDir:        logDir,
		updateChecker: NewUpdateChecker(),
		vault:         vault,
		opdsCatalog:   opds.NewCatalog(store),
		downloads:     download.NewTracker(),
	}
}

// SetTokenStore wires the bearer-token store. Called once at startup from
// main.go after the api.TokenStore is constructed.
func (h *Handler) SetTokenStore(t *api.TokenStore) { h.tokens = t }

// SetUserIDResolver wires the session-aware user-ID resolver. Called once at
// startup from main.go.
func (h *Handler) SetUserIDResolver(fn func(*http.Request) (int64, bool)) { h.userIDOf = fn }

func (h *Handler) SetArchiver(a *worker.Archiver) {
	h.archiver = a
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

const maxUploadSize = 10 << 20
