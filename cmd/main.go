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
	"golang.org/x/time/rate"

	"github.com/linuxnoodle/webfictionpoller/internal/auth"
	"github.com/linuxnoodle/webfictionpoller/internal/database"
	"github.com/linuxnoodle/webfictionpoller/internal/handlers"
	"github.com/linuxnoodle/webfictionpoller/internal/logging"
	"github.com/linuxnoodle/webfictionpoller/internal/models"
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

	sessionManager := scs.New()
	sessionManager.Store = sqlite3store.New(db)
	sessionManager.Lifetime = 24 * time.Hour
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteLaxMode
	sessionManager.Cookie.Secure = os.Getenv("COOKIE_SECURE") != "false"

	providerList := []providers.Provider{
		providers.NewRoyalRoadProvider(),
		providers.NewSpaceBattlesProvider(),
		providers.NewSufficientVelocityProvider(),
		providers.NewQuestionableQuestingProvider(),
		providers.NewFanfictionNetProvider(),
	}

	store := handlers.NewStore(db)

	pool := worker.NewWorkerPool(4, providerList, func(seriesID int64, chapters []models.Chapter) {
		_, err := store.InsertChapters(seriesID, chapters)
		if err != nil {
			logging.Error("error inserting chapters for series %d: %v", seriesID, err)
		}
	})

	loadProviderCookies(store, pool)

	if err := handlers.InitTemplates(); err != nil {
		log.Fatalf("failed to load templates: %v", err)
	}

	faviconCache := handlers.NewFaviconCache()

	h := handlers.NewHandler(store, pool, logDir)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(sessionManager.LoadAndSave)
	r.Use(securityHeaders)

	r.Get("/login", loginPage)
	r.Post("/login", loginRateLimiter(loginHandler(db, sessionManager)))
	r.Get("/setup", setupPage(db))
	r.Post("/setup", setupHandler(db, sessionManager))

	r.Get("/static/app.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(static.CSS)
	})

	r.Get("/static/favicons/{provider}", faviconCache.ServeHTTP)

	r.Post("/logout", logoutHandler(sessionManager))

	r.Group(func(r chi.Router) {
		r.Use(authMiddleware(sessionManager, db))

		r.Get("/", h.Dashboard)
		r.Get("/series", h.SeriesList)
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

		r.Get("/api/chapters/time", h.TimePagePartial)
		r.Get("/api/chapters/{id}/preview", h.ChapterPreview)
		r.Post("/api/chapters/{id}/read", h.MarkChapterRead)
		r.Get("/api/logs", h.LogsData)
		r.Post("/api/series/{id}/status", h.UpdateSeriesStatus)
		r.Post("/api/series/{id}/rating", h.UpdateSeriesRating)
		r.Post("/api/series/{id}/read-all", h.MarkAllRead)
		r.Post("/api/read-all", h.MarkAllChaptersRead)
		r.Post("/api/series/{id}/delete", h.DeleteSeries)
		r.Post("/api/poll", h.PollNow)
		r.Get("/api/search", h.SearchSeries)
		r.Get("/api/version", h.VersionAPI)
		r.Post("/api/version/check", h.VersionCheckNow)
		r.Post("/api/version/update", h.SelfUpdate)
		r.Get("/admin/version", h.VersionPage)
	})

	csrfHandler := nosurf.New(r)
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

func securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline' https://unpkg.com; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: https:; " +
		"connect-src 'self'; " +
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

type ipLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func newIPLimiter() *ipLimiter {
	return &ipLimiter{limiters: make(map[string]*rate.Limiter)}
}

func (il *ipLimiter) get(ip string) *rate.Limiter {
	il.mu.Lock()
	defer il.mu.Unlock()
	if l, ok := il.limiters[ip]; ok {
		return l
	}
	l := rate.NewLimiter(rate.Every(time.Second), 5)
	il.limiters[ip] = l
	return l
}

func (il *ipLimiter) cleanup() {
	il.mu.Lock()
	defer il.mu.Unlock()
	il.limiters = make(map[string]*rate.Limiter)
}

var loginLimiter = newIPLimiter()

func init() {
	go func() {
		for range time.Tick(10 * time.Minute) {
			loginLimiter.cleanup()
		}
	}()
}

func loginRateLimiter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := r.RemoteAddr
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			ip = fwd
		}
		if !loginLimiter.get(ip).Allow() {
			handlers.RenderLoginPage(w, r, map[string]interface{}{"Error": "Too many login attempts. Please wait."})
			return
		}
		next(w, r)
	}
}

func loadProviderCookies(store *handlers.Store, pool *worker.WorkerPool) {
	for name, p := range pool.AllProviders() {
		if !p.RequiresAuth() {
			continue
		}
		pc, err := store.GetProviderConfig(name)
		if err != nil || pc == nil || pc.CookieData == "" {
			continue
		}
		if err := p.SetCookies(pc.CookieData); err != nil {
			logging.Error("warning: failed to set cookies for %s: %v", name, err)
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

func loginPage(w http.ResponseWriter, r *http.Request) {
	handlers.RenderLoginPage(w, r, nil)
}

func loginHandler(db *sql.DB, sm *scs.SessionManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.FormValue("username")
		password := r.FormValue("password")
		id, err := auth.Authenticate(db, username, password)
		if err != nil {
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
