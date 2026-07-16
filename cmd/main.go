package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/alexedwards/scs/sqlite3store"
	"github.com/alexedwards/scs/v2"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/justinas/nosurf"

	"github.com/linuxnoodle/webfictionpoller/internal/auth"
	"github.com/linuxnoodle/webfictionpoller/internal/blob"
	"github.com/linuxnoodle/webfictionpoller/internal/comics"
	"github.com/linuxnoodle/webfictionpoller/internal/crypto"
	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/handlers"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
	"github.com/linuxnoodle/webfictionpoller/internal/plugin"
	"github.com/linuxnoodle/webfictionpoller/internal/providers"
	"github.com/linuxnoodle/webfictionpoller/internal/static"
	"github.com/linuxnoodle/webfictionpoller/internal/worker"
)

func main() {
	dbPath := envOrDefault("DB_PATH", "data.db")
	addr := envOrDefault("ADDR", ":8080")
	pollInterval := envOrDefault("POLL_INTERVAL", "15m")
	logDir := envOrDefault("LOG_DIR", "data/logs")

	if err := logging.Init(logDir); err != nil {
		log.Fatalf("failed to init logging: %v", err)
	}
	defer logging.Close()
	logging.Info("starting webfiction_poller")

	db, err := database.InitDB(dbPath)
	if err != nil {
		logging.Error("failed to init database: %v", err)
		log.Fatalf("failed to init database: %v", err)
	}
	defer db.Close()

	vault, err := crypto.OpenVault(envOrDefault("SECRET_KEY_PATH", "data/secret.key"))
	if err != nil {
		logging.Error("failed to init encryption vault: %v", err)
		log.Fatalf("failed to init encryption vault: %v", err)
	}

	sessionManager := scs.New()
	sessionManager.Store = sqlite3store.New(db)
	sessionManager.Lifetime = 30 * 24 * time.Hour
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = os.Getenv("COOKIE_SECURE") != "false"

	// Providers self-register via init() in internal/providers and internal/comics.
	// Derive the runtime list from the global plugin registry.
	providerList := registryTextProviders()
	for _, p := range plugin.Default.ByKind(plugin.KindComic) {
		if cp, ok := p.(comics.ComicProvider); ok {
			handlers.RegisterComicProvider(cp)
		}
	}

	store := handlers.NewStore(db)

	blobStore, err := blob.FromConfig(context.Background(), blob.FromEnv())
	if err != nil {
		logging.Error("failed to init blob store: %v", err)
		log.Fatalf("failed to init blob store: %v", err)
	}
	handlers.SetBlobStore(blobStore)
	logging.Info("blob store initialized: backend=%s", blob.FromEnv().Backend)

	pool := worker.NewWorkerPool(4, providerList, func(seriesID int64, chapters []models.Chapter) {
		_, err := store.InsertChapters(seriesID, chapters)
		if err != nil {
			logging.Error("error inserting chapters for series %d: %v", seriesID, err)
		}
	})

	loadProviderCredentials(store, pool, vault)

	if err := handlers.InitTemplates(); err != nil {
		log.Fatalf("failed to load templates: %v", err)
	}

	faviconCache := handlers.NewFaviconCache()

	h := handlers.NewHandler(store, pool, logDir, vault)

	archiver := worker.NewArchiver(store, providerList, envOrDefault("ARCHIVE_ALL", "false") == "true")
	h.SetArchiver(archiver)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Group(func(r chi.Router) {
		r.Use(opdsBasicAuth(db))
		r.Get("/opds", h.OPDSRoot)
		r.Get("/opds/cover/{id}", h.OPDSCover)
		r.Get("/opds/epub/{id}", h.OPDSEpub)
		r.Get("/opds/images/{chapterId}/{url}", h.OPDSImage)
	})

	r.Group(func(r chi.Router) {
		r.Use(sessionManager.LoadAndSave)
		r.Use(securityHeaders)

		r.Get("/login", loginPage)
		r.Post("/login", loginBanMiddleware(loginHandler(db, sessionManager)))
		r.Get("/setup", setupPage(db))
		r.Post("/setup", setupHandler(db, sessionManager))

		r.Get("/static/app.css", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(static.CSS)
		})

		r.Get("/static/htmx.min.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(static.HTMX)
		})

		r.Get("/static/alpine.min.js", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.Write(static.Alpine)
		})

		r.Get("/static/favicons/{provider}", faviconCache.ServeHTTP)

		r.Post("/logout", logoutHandler(sessionManager))

		r.Group(func(r chi.Router) {
			r.Use(authMiddleware(sessionManager, db))

			r.Get("/", h.Dashboard)
			r.Get("/series/add", h.AddSeriesPage)
			r.Post("/series/add", h.AddSeries)
			r.Get("/series/import", h.ImportOPMLPage)
			r.Post("/series/import", h.ImportOPML)
			r.Get("/series/export", h.ExportOPML)
			r.Get("/series/backup", h.ExportBackup)
			r.Post("/series/backup", h.ImportBackup)
			r.Get("/admin/providers", h.ProviderConfigPage)
			r.Post("/admin/providers", h.SaveProviderConfig)
			r.Get("/admin/logs", h.LogsPage)
			r.Get("/admin/library", h.LibraryPage)

			r.Get("/api/chapters/time", h.TimePagePartial)
			r.Get("/api/chapters/{id}/preview", h.ChapterPreview)
			r.Post("/api/chapters/{id}/read", h.MarkChapterRead)
			r.Get("/api/logs", h.LogsData)
			r.Post("/api/series/{id}/status", h.UpdateSeriesStatus)
			r.Post("/api/series/{id}/rating", h.UpdateSeriesRating)
			r.Post("/api/series/{id}/read-all", h.MarkAllRead)
			r.Post("/api/read-all", h.MarkAllChaptersRead)
			r.Get("/api/unread-count", h.UnreadCountAPI)
			r.Post("/api/series/{id}/delete", h.DeleteSeries)
			r.Post("/api/series/{id}/sync", h.SyncSeriesNow)
			r.Post("/api/poll", h.PollNow)
			r.Get("/api/poll/progress", h.PollProgress)
			r.Post("/api/providers/check-auth", h.CheckAuthProvider)
			r.Get("/api/providers/password", h.GetProviderPassword)
			r.Post("/api/series/{id}/archive", h.UpdateSeriesArchive)
			r.Post("/api/series/{id}/archive/delete", h.DeleteSeriesArchive)
			r.Post("/api/series/{id}/archive/re-archive", h.ReArchiveSeries)
			r.Get("/api/archive/status", h.ArchiverStatusAPI)
			r.Get("/api/archive/all", h.ArchiveAllAPI)
			r.Post("/api/archive/all", h.ArchiveAllAPI)
			r.Post("/api/archive/run", h.TriggerArchiveNow)
			r.Get("/api/archive/storage", h.StorageInfoAPI)
			r.Delete("/api/reader/chapter/{id}/archive", h.DeleteChapterArchive)
			r.Get("/api/search", h.SearchSeries)
			r.Get("/api/version", h.VersionAPI)
			r.Post("/api/version/check", h.VersionCheckNow)
			r.Post("/api/version/update", h.SelfUpdate)
			r.Get("/admin/version", h.VersionPage)

			r.Get("/reader/{id}", h.ReaderPage)
			r.Get("/api/reader/{id}/chapters", h.ReaderChaptersAPI)
			r.Get("/api/reader/chapter/{id}", h.ReaderChapterContentAPI)
			r.Get("/api/reader/chapter/{id}/comments", h.ReaderChapterCommentsAPI)
			r.Post("/api/reader/progress", h.ReaderSaveProgressAPI)
			r.Get("/api/reader/settings", h.ReaderSettingsAPI)
			r.Put("/api/reader/settings", h.ReaderSettingsAPI)

			r.Get("/comics", h.ComicBrowsePage)
			r.Get("/comics/read/{id}", h.ComicReaderPage)
			r.Get("/api/comics/search", h.ComicSearchAPI)
			r.Post("/api/comics/series", h.ComicAddSeriesAPI)
			r.Get("/api/comics/library", h.ComicLibraryAPI)
			r.Get("/api/comics/series/{id}", h.ComicSeriesDetailAPI)
			r.Delete("/api/comics/series/{id}", h.ComicDeleteSeriesAPI)
			r.Post("/api/comics/series/{id}/refresh", h.ComicRefreshChaptersAPI)
			r.Post("/api/comics/series/{id}/read-all", h.ComicMarkAllReadAPI)
			r.Post("/api/comics/series/{id}/rating", h.ComicUpdateRatingAPI)
			r.Post("/api/comics/series/{id}/status", h.ComicUpdateStatusAPI)
			r.Get("/api/comics/chapter/{id}/pages", h.ComicChapterPagesAPI)
			r.Post("/api/comics/chapter/{id}/read", h.ComicMarkReadAPI)
			r.Post("/api/comics/chapter/{id}/download", h.ComicDownloadChapterAPI)
			r.Get("/comics/page/{chapterId}/{pageIndex}", h.ComicServePage)
			r.Post("/api/comics/progress", h.ComicSaveProgressAPI)
		})
	})

	csrfHandler := nosurf.New(r)
	csrfHandler.SetBaseCookie(http.Cookie{
		Path:     "/",
		MaxAge:   31536000,
		HttpOnly: true,
		Secure:   os.Getenv("COOKIE_SECURE") != "false",
		SameSite: http.SameSiteLaxMode,
	})
	csrfHandler.ExemptFunc(func(r *http.Request) bool {
		return r.Method == "GET" || r.Method == "HEAD" || r.Method == "OPTIONS"
	})

	interval, err := time.ParseDuration(pollInterval)
	if err != nil {
		log.Fatalf("invalid POLL_INTERVAL: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	scheduler := newScheduler(interval, store, pool, logDir)
	go scheduler.start(ctx)

	archiveInterval := envOrDefault("ARCHIVE_INTERVAL", "1h")
	archiveDur, _ := time.ParseDuration(archiveInterval)
	if archiveDur == 0 {
		archiveDur = time.Hour
	}
	go archiver.Run(ctx, archiveDur)

	logging.Info("starting server on %s (poll interval: %s)", addr, interval)
	server := &http.Server{Addr: addr, Handler: csrfHandler}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		logging.Info("received signal %s, shutting down...", sig)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logging.Error("server shutdown error: %v", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("server error: %v", err)
	}

	pool.Stop()
	logging.Info("shutdown complete")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// registryTextProviders returns every registered text provider that implements
// the legacy providers.Provider interface. This is the bridge between the new
// plugin.Registry and the existing worker pool / handlers, which still consume
// providers.Provider. As those consumers migrate to plugin capability
// interfaces directly, this helper goes away.
func registryTextProviders() []providers.Provider {
	var out []providers.Provider
	for _, p := range plugin.Default.ByKind(plugin.KindText) {
		if lp, ok := p.(providers.Provider); ok {
			out = append(out, lp)
		} else {
			logging.Error("[main] text provider %q does not implement legacy providers.Provider interface; skipping", p.Meta().Name)
		}
	}
	if len(out) == 0 {
		logging.Error("[main] no text providers registered; check that internal/providers is imported")
	}
	return out
}

func securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' 'unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https:; " +
		"connect-src 'self' https://api.mangadex.org https://*.mangadex.network; " +
		"frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

type loginBan struct {
	mu    sync.Mutex
	fails map[string]int
	bans  map[string]time.Time
}

var loginBans = &loginBan{
	fails: make(map[string]int),
	bans:  make(map[string]time.Time),
}

func init() {
	go func() {
		for range time.Tick(5 * time.Minute) {
			loginBans.mu.Lock()
			loginBans.fails = make(map[string]int)
			now := time.Now()
			for ip, t := range loginBans.bans {
				if now.Sub(t) > 15*time.Minute {
					delete(loginBans.bans, ip)
				}
			}
			loginBans.mu.Unlock()
		}
	}()
}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fwd
	}
	return r.RemoteAddr
}

func (lb *loginBan) isBanned(ip string) bool {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if t, ok := lb.bans[ip]; ok {
		if time.Since(t) < 15*time.Minute {
			return true
		}
		delete(lb.bans, ip)
	}
	return false
}

func (lb *loginBan) recordFail(ip string) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.fails[ip]++
	if lb.fails[ip] >= 5 {
		lb.bans[ip] = time.Now()
		delete(lb.fails, ip)
		logging.Info("[auth] banned %s for 15m after %d failed logins", ip, 5)
	}
}

func loginBanMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if loginBans.isBanned(ip) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func loadProviderCredentials(store *handlers.Store, pool *worker.WorkerPool, vault *crypto.Vault) {
	for name, p := range pool.AllProviders() {
		if !p.RequiresAuth() {
			continue
		}
		pc, err := store.GetProviderConfig(name)
		if err != nil || pc == nil {
			continue
		}

		if lr, ok := p.(providers.LoginRefresher); ok {
			providerName := name
			lr.SetCredentialSource(func() (string, string, bool) {
				fresh, err := store.GetProviderConfig(providerName)
				if err != nil || fresh == nil {
					return "", "", false
				}
				if fresh.Username == "" || fresh.EncryptedPassword == "" {
					return "", "", false
				}
				plainPass, err := vault.Decrypt(fresh.EncryptedPassword)
				if err != nil {
					logging.Error("failed to decrypt password for %s during re-login: %v", providerName, err)
					return "", "", false
				}
				return fresh.Username, plainPass, true
			})
		}

		if pc.Username != "" && pc.EncryptedPassword != "" && p.SupportsLogin() {
			plainPass, err := vault.Decrypt(pc.EncryptedPassword)
			if err != nil {
				logging.Error("warning: failed to decrypt password for %s: %v", name, err)
			} else if err := p.Login(pc.Username, plainPass); err != nil {
				logging.Error("warning: login failed for %s: %v", name, err)
			} else {
				continue
			}
		}

		if pc.CookieData != "" {
			if err := p.SetCookies(pc.CookieData); err != nil {
				logging.Error("warning: failed to set cookies for %s: %v", name, err)
			}
		}
	}
}

func authMiddleware(sm *scs.SessionManager, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hasUsers, err := auth.HasUsers(db)
			if err == nil && !hasUsers {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
				return
			}
			if sm.Get(r.Context(), "userID") == nil {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func opdsBasicAuth(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hasUsers, err := auth.HasUsers(db)
			if err != nil || !hasUsers {
				next.ServeHTTP(w, r)
				return
			}
			username, password, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="WebFiction Poller OPDS"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			_, err = auth.Authenticate(db, username, password)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Basic realm="WebFiction Poller OPDS"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func loginPage(w http.ResponseWriter, r *http.Request) {
	handlers.RenderLoginPage(w, r, nil)
}

func loginHandler(db *sql.DB, sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.FormValue("username")
		password := r.FormValue("password")
		id, err := auth.Authenticate(db, username, password)
		if err != nil {
			loginBans.recordFail(clientIP(r))
			handlers.RenderLoginPage(w, r, map[string]interface{}{"Error": "Invalid username or password"})
			return
		}
		sm.Put(r.Context(), "userID", id)
		sm.Put(r.Context(), "username", username)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func setupPage(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasUsers, err := auth.HasUsers(db)
		if err == nil && hasUsers {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		handlers.RenderSetupPage(w, r, nil)
	}
}

func setupHandler(db *sql.DB, sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hasUsers, err := auth.HasUsers(db)
		if err == nil && hasUsers {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		username := r.FormValue("username")
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")
		if username == "" || password == "" {
			handlers.RenderSetupPage(w, r, map[string]interface{}{"Error": "Username and password are required"})
			return
		}
		if password != confirm {
			handlers.RenderSetupPage(w, r, map[string]interface{}{"Error": "Passwords do not match"})
			return
		}
		if err := auth.CreateUser(db, username, password); err != nil {
			handlers.RenderSetupPage(w, r, map[string]interface{}{"Error": "Failed to create account"})
			return
		}
		id, err := auth.Authenticate(db, username, password)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		sm.Put(r.Context(), "userID", id)
		sm.Put(r.Context(), "username", username)
		logging.Info("initial account %q created", username)
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func logoutHandler(sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sm.Destroy(r.Context())
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

type scheduler struct {
	interval time.Duration
	store    *handlers.Store
	pool     *worker.WorkerPool
	logDir   string
}

func newScheduler(interval time.Duration, store *handlers.Store, pool *worker.WorkerPool, logDir string) *scheduler {
	return &scheduler{interval: interval, store: store, pool: pool, logDir: logDir}
}

func (s *scheduler) start(ctx context.Context) {
	s.runPoll(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logging.Info("[scheduler] stopping")
			return
		case <-ticker.C:
			s.runPoll(ctx)
		}
	}
}

func (s *scheduler) runPoll(ctx context.Context) {
	logging.RotateIfNeeded(s.logDir)
	select {
	case <-ctx.Done():
		return
	default:
	}

	all, err := s.store.GetAllActiveSeries()
	if err != nil {
		logging.Error("[scheduler] error fetching series: %v", err)
		return
	}
	logging.Info("[scheduler] polling %d series", len(all))
	for _, series := range all {
		select {
		case <-ctx.Done():
			return
		default:
		}
		p, ok := s.pool.GetProvider(series.ProviderName)
		if !ok {
			continue
		}
		s.pool.Submit(worker.Job{Series: series, Provider: p})
	}
}
