package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/linqiu919/better-context-engine/internal/auth"
	"github.com/linqiu919/better-context-engine/internal/config"
	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/events"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/indexer"
	"github.com/linqiu919/better-context-engine/internal/store"
	webassets "github.com/linqiu919/better-context-engine/web"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type contextKey string

const userKey contextKey = "user"
const sessionKey contextKey = "session"

type Server struct {
	config  config.Config
	store   store.Store
	auth    *auth.Service
	indexer *indexer.Service
	events  *events.Broker
	logger  *slog.Logger

	// quotaMu guards the in-process rate-limit window, the per-user storage
	// cache and the login-failure counters; all are best-effort accelerators
	// that reset naturally on restart. When Redis is configured they act as
	// the fallback path only.
	quotaMu        sync.Mutex
	rpmWindow      time.Time
	rpmCounts      map[string]int
	storageCache   map[string]storageCacheEntry
	loginFails     map[string]failEntry
	regCodes       map[string]regCodeEntry
	abuse          map[string]*abuseEntry
	redis          *redis.Client
	redisDownUntil time.Time

	// Memoized admin analytics payload (analytics.go): rebuilt at most once
	// per analyticsCacheTTL regardless of how many admin tabs poll it.
	analyticsMu    sync.Mutex
	analyticsAt    time.Time
	analyticsCache *analyticsResponse

	// searchGroup collapses concurrent identical searches (same cache key:
	// checkpoint + query + output cap) into one pipeline run whose result all
	// callers share. ACE clients have been observed firing the same retrieval
	// 4x within a second; on a 2-core host those runs starve each other. The
	// search cache cannot cover this window — it is only populated after the
	// first run completes.
	searchGroup singleflight.Group
}

type storageCacheEntry struct {
	bytes   int64
	expires time.Time
}

func New(cfg config.Config, st store.Store, authService *auth.Service, idx *indexer.Service, broker *events.Broker, logger *slog.Logger) http.Handler {
	s := &Server{config: cfg, store: st, auth: authService, indexer: idx, events: broker, logger: logger, rpmCounts: map[string]int{}, storageCache: map[string]storageCacheEntry{}, loginFails: map[string]failEntry{}, regCodes: map[string]regCodeEntry{}, abuse: map[string]*abuseEntry{}, redis: newRedisClient(cfg.RedisAddr, cfg.RedisPassword)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("GET /api/v1/auth/config", s.authConfig)
	mux.HandleFunc("POST /api/v1/auth/register/send-code", s.sendRegisterCode)
	mux.HandleFunc("POST /api/v1/auth/register", s.register)
	mux.HandleFunc("GET /api/v1/auth/linuxdo", s.linuxdoStart)
	mux.HandleFunc("GET /api/v1/auth/linuxdo/callback", s.linuxdoCallback)
	mux.Handle("POST /batch-upload", s.aceAuth(http.HandlerFunc(s.batchUpload)))
	mux.Handle("POST /agents/codebase-retrieval", s.aceAuth(http.HandlerFunc(s.aceSearch)))
	mux.Handle("POST /prompt-enhancer", s.aceAuth(http.HandlerFunc(s.promptEnhance)))

	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/me", s.me)
	api.HandleFunc("PATCH /api/v1/me/settings", s.updateMySettings)
	api.HandleFunc("POST /api/v1/me/ace-token", s.generateACEToken)
	api.HandleFunc("GET /api/v1/me/ace-token", s.currentACEToken)
	api.HandleFunc("GET /api/v1/me/ace-projects", s.myACEProjects)
	api.HandleFunc("DELETE /api/v1/me/ace-projects/{name}", s.deleteMyACEProject)
	api.HandleFunc("GET /api/v1/me/ace-usage", s.myACEUsage)
	api.HandleFunc("GET /api/v1/me/quota", s.meQuota)
	api.HandleFunc("GET /api/v1/me/announcement", s.myAnnouncement)
	api.HandleFunc("POST /api/v1/me/announcement/dismiss", s.dismissAnnouncement)
	api.HandleFunc("POST /api/v1/auth/logout", s.logout)
	api.HandleFunc("GET /api/v1/overview", s.overview)
	api.HandleFunc("GET /api/v1/repositories", s.repositories)
	api.HandleFunc("POST /api/v1/repositories", s.createRepository)
	api.HandleFunc("GET /api/v1/repositories/{id}", s.repository)
	api.HandleFunc("GET /api/v1/repositories/{id}/metrics", s.repositoryMetrics)
	api.HandleFunc("GET /api/v1/repositories/{id}/sync-jobs", s.repositoryJobs)
	api.HandleFunc("POST /api/v1/repositories/{id}/sync", s.syncRepository)
	api.HandleFunc("POST /api/v1/repositories/{id}/pause", s.pauseRepository)
	api.HandleFunc("GET /api/v1/activity", s.activity)
	api.HandleFunc("POST /api/v1/search/inspect", s.inspectSearch)
	api.HandleFunc("GET /api/v1/repositories/{id}/eval-cases", s.listEvalCases)
	api.HandleFunc("POST /api/v1/repositories/{id}/eval-cases", s.createEvalCase)
	api.HandleFunc("DELETE /api/v1/repositories/{id}/eval-cases/{caseId}", s.deleteEvalCase)
	api.HandleFunc("POST /api/v1/repositories/{id}/eval-run", s.runEval)
	api.HandleFunc("GET /api/v1/events", s.eventStream)
	api.HandleFunc("GET /api/v1/admin/overview", s.adminOnly(s.adminOverview))
	api.HandleFunc("GET /api/v1/admin/analytics", s.adminOnly(s.adminAnalytics))
	api.HandleFunc("GET /api/v1/admin/users", s.adminOnly(s.adminUsers))
	api.HandleFunc("GET /api/v1/admin/ace-usage", s.adminOnly(s.adminACEUsage))
	api.HandleFunc("GET /api/v1/admin/ace-projects", s.adminOnly(s.adminACEProjects))
	api.HandleFunc("PATCH /api/v1/admin/users/{id}", s.adminOnly(s.adminUpdateUser))
	api.HandleFunc("POST /api/v1/admin/users/{id}/reset-quota", s.adminOnly(s.adminResetQuota))
	api.HandleFunc("GET /api/v1/admin/settings/quota", s.adminOnly(s.getQuotaSettings))
	api.HandleFunc("PATCH /api/v1/admin/settings/quota", s.adminOnly(s.updateQuotaSettings))
	api.HandleFunc("GET /api/v1/admin/repositories", s.adminOnly(s.adminRepositories))
	api.HandleFunc("GET /api/v1/admin/index-jobs", s.adminOnly(s.adminJobs))
	api.HandleFunc("POST /api/v1/admin/index-jobs/{id}/retry", s.adminOnly(s.retryJob))
	api.HandleFunc("POST /api/v1/admin/index-jobs/{id}/cancel", s.adminOnly(s.cancelJob))
	api.HandleFunc("GET /api/v1/admin/infrastructure", s.adminOnly(s.infrastructure))
	api.HandleFunc("GET /api/v1/admin/settings/retrieval", s.adminOnly(s.getRetrievalSettings))
	api.HandleFunc("PATCH /api/v1/admin/settings/retrieval", s.adminOnly(s.updateRetrievalSettings))
	api.HandleFunc("GET /api/v1/admin/settings/system", s.adminOnly(s.getSystemSettings))
	api.HandleFunc("PATCH /api/v1/admin/settings/system", s.adminOnly(s.updateSystemSettings))
	api.HandleFunc("GET /api/v1/admin/announcements", s.adminOnly(s.adminListAnnouncements))
	api.HandleFunc("POST /api/v1/admin/announcements", s.adminOnly(s.adminCreateAnnouncement))
	api.HandleFunc("DELETE /api/v1/admin/announcements/{id}", s.adminOnly(s.adminDeleteAnnouncement))
	api.HandleFunc("GET /api/v1/admin/audit-log", s.adminOnly(s.auditLog))
	mux.Handle("/api/v1/", s.authenticate(api))
	mux.Handle("/", webassets.Handler())
	return securityHeaders(requestLogger(logger, mux))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		// challenges.cloudflare.com hosts the Turnstile widget script and its
		// iframe; avatars (LinuxDo) may come from any https host.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; connect-src 'self'; script-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com")
		next.ServeHTTP(w, r)
	})
}
func requestLogger(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("bce_session")
		if err != nil {
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		user, session, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid session")
			return
		}
		if mutating(r.Method) && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(session.CSRFToken)) != 1 {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		ctx := context.WithValue(r.Context(), userKey, user)
		ctx = context.WithValue(ctx, sessionKey, session)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// aceAuth accepts either the shared deployment token (no user attribution)
// or a personal ACE token, which is resolved to its owner and attached to the
// request context so downstream handlers can attribute checkpoints and audit.
func (s *Server) aceAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			writeError(w, http.StatusUnauthorized, "invalid ACE token")
			return
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.config.ACEToken)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		user, err := s.store.UserByACETokenHash(r.Context(), hashToken(got))
		if err != nil || user.Disabled {
			writeError(w, http.StatusUnauthorized, "invalid ACE token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	})
}

// userMaxOutput resolves the retrieval output budget for the calling user:
// the account preference (max_output_tokens, 0 = server default) caps whatever
// the client requests, so no single retrieval can flood an agent's context.
func userMaxOutput(r *http.Request, requested int) int {
	limit := currentUser(r).MaxOutputTokens
	if limit <= 0 {
		limit = indexer.DefaultMaxOutputTokens
	}
	if requested <= 0 || requested > limit {
		return limit
	}
	return requested
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// recordACEUsage tallies an ACE/MCP call for the attributed user (empty ID =
// shared deployment token). Usage accounting is best-effort and never fails
// the request.
func (s *Server) recordACEUsage(r *http.Request, endpoint string, units int64) {
	_ = s.store.RecordACEUsage(r.Context(), currentUser(r).ID, endpoint, units)
}

// quotaSettings resolves the deployment quota configuration; every limit is
// strictly positive, so the positive-or-default merge applies to all keys.
func (s *Server) quotaSettings(ctx context.Context) domain.QuotaSettings {
	values, _ := s.store.GetSettings(ctx)
	get := func(key string, def int) int {
		if v, err := strconv.Atoi(values[key]); err == nil && v > 0 {
			return v
		}
		return def
	}
	return domain.QuotaSettings{
		StorageLimitMB: get("quota_storage_mb", 100),
		DailyRetrieval: get("quota_daily_retrieval", 100),
		DailyEnhance:   get("quota_daily_enhance", 50),
		DailyUpload:    get("quota_daily_upload", 500),
		RPMRetrieval:   get("rpm_retrieval", 30),
		RPMUpload:      get("rpm_upload", 120),
		RPMEnhance:     get("rpm_enhance", 10),
	}
}

// enforceQuota applies the per-user RPM window, the daily call allowance and
// (for uploads) the storage ceiling before an ACE endpoint does any work.
// Admin users and shared-token callers (no user attribution) are exempt.
// Returns false after writing the refusal response. Checks are read-only:
// the counter only advances via bumpQuota once the request succeeds.
func (s *Server) enforceQuota(w http.ResponseWriter, r *http.Request, kind string, incomingBytes int64) bool {
	user := currentUser(r)
	if user.ID == "" || user.Role == domain.RoleAdmin {
		return true
	}
	q := s.quotaSettings(r.Context())
	var rpm, daily int
	switch kind {
	case "retrieval":
		rpm, daily = q.RPMRetrieval, q.DailyRetrieval
	case "enhance":
		rpm, daily = q.RPMEnhance, q.DailyEnhance
	case "upload":
		rpm, daily = q.RPMUpload, q.DailyUpload
	}
	if !s.allowRPM(r.Context(), user.ID, kind, rpm) {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("rate limit exceeded: at most %d %s requests per minute, please retry later", rpm, kind))
		return false
	}
	used, usedErr := s.store.QuotaUsageToday(r.Context(), user.ID)
	if usedErr == nil && used[kind] >= int64(daily) {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("daily %s quota exhausted (%d per day); it resets at midnight, or an administrator can reset it", kind, daily))
		return false
	}
	if kind == "upload" {
		limit := int64(q.StorageLimitMB) << 20
		// Persistent backstop: uploads before the first retrieval are invisible
		// to snapshot-based storage totals (the project archives on retrieval),
		// so a per-day byte counter bounds them. A user whose total storage is
		// capped at N MB can never legitimately upload more than that in a day;
		// project deletion clears the counter.
		if usedErr == nil && used["upload_bytes"]+incomingBytes > limit {
			s.setQuotaBlock(r.Context(), user.ID)
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("daily upload volume exceeds the storage limit (%d MB); it resets at midnight, or remove projects to free the allowance", q.StorageLimitMB))
			return false
		}
		if s.userStorageBytes(r.Context(), user.ID)+incomingBytes > limit {
			s.setQuotaBlock(r.Context(), user.ID)
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("storage limit exceeded (%d MB); remove projects or ask an administrator to raise the limit", q.StorageLimitMB))
			return false
		}
	}
	return true
}

// allowRPM consumes one slot of the caller's fixed 60-second window, in
// Redis when configured (correct across instances and restarts), otherwise
// process-local — where a restart forgives at most one minute of counts.
func (s *Server) allowRPM(ctx context.Context, userID, kind string, limit int) bool {
	if s.redisUsable() {
		if ok, handled := s.allowRPMRedis(ctx, userID, kind, limit); handled {
			return ok
		}
	}
	now := time.Now()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if now.Sub(s.rpmWindow) >= time.Minute {
		s.rpmWindow = now
		s.rpmCounts = map[string]int{}
	}
	key := userID + "|" + kind
	if s.rpmCounts[key] >= limit {
		return false
	}
	s.rpmCounts[key]++
	return true
}

// userStorageBytes sums the storage of the user's archived ACE projects,
// cached for a minute so upload bursts don't recompute snapshot aggregates
// on every call. Deduplicated blobs mean the sum slightly overstates unique
// storage across projects — acceptable for a ceiling check.
func (s *Server) userStorageBytes(ctx context.Context, userID string) int64 {
	if s.redisUsable() {
		if total, handled := s.storageBytesRedis(ctx, userID); handled {
			return total
		}
	}
	now := time.Now()
	s.quotaMu.Lock()
	if entry, ok := s.storageCache[userID]; ok && now.Before(entry.expires) {
		s.quotaMu.Unlock()
		return entry.bytes
	}
	s.quotaMu.Unlock()
	total := s.computeStorageBytes(ctx, userID)
	s.quotaMu.Lock()
	s.storageCache[userID] = storageCacheEntry{bytes: total, expires: now.Add(storageCacheTTL)}
	s.quotaMu.Unlock()
	return total
}

// computeStorageBytes is the database truth behind both cache layers.
func (s *Server) computeStorageBytes(ctx context.Context, userID string) int64 {
	var total int64
	if projects, err := s.indexer.ListACEProjects(ctx, userID); err == nil {
		for _, p := range projects {
			total += p.StorageBytes
		}
	}
	return total
}

// noteStoredBytes folds a successful upload's estimated stored size into the
// caller's cached storage total, so bursts inside the cache window see the
// accumulating footprint rather than a stale snapshot. Each bump refreshes
// the entry's TTL: pre-archival uploads are invisible to the database truth,
// so letting the entry expire mid-session would zero the accumulated estimate
// and unbound the ceiling check. Once uploads pause for a TTL the database
// truth wins again (deduplicated blobs make the estimate conservative).
func (s *Server) noteStoredBytes(r *http.Request, n int64) {
	user := currentUser(r)
	if user.ID == "" || user.Role == domain.RoleAdmin {
		return
	}
	if s.redisUsable() && s.noteStoredBytesRedis(r.Context(), user.ID, n) {
		return
	}
	s.quotaMu.Lock()
	if entry, ok := s.storageCache[user.ID]; ok {
		entry.bytes += n
		entry.expires = time.Now().Add(storageCacheTTL)
		s.storageCache[user.ID] = entry
	}
	s.quotaMu.Unlock()
}

// bumpQuota advances today's counter after a successful, attributed,
// non-admin request; like usage accounting it never fails the request.
func (s *Server) bumpQuota(r *http.Request, kind string) {
	if user := currentUser(r); user.ID != "" && user.Role != domain.RoleAdmin {
		_ = s.store.IncQuotaUsage(r.Context(), user.ID, kind)
	}
}

// meQuota backs the account page's usage panel: the caller's limits plus
// today's consumption and current storage footprint.
func (s *Server) meQuota(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	used, _ := s.store.QuotaUsageToday(r.Context(), user.ID)
	writeJSON(w, 200, map[string]any{
		"limits":    s.quotaSettings(r.Context()),
		"unlimited": user.Role == domain.RoleAdmin,
		"usage": map[string]any{
			"storage_bytes": s.userStorageBytes(r.Context(), user.ID),
			"retrieval":     used["retrieval"],
			"enhance":       used["enhance"],
			"upload":        used["upload"],
		},
	})
}
func (s *Server) adminOnly(handler func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if currentUser(r).Role != domain.RoleAdmin {
			writeError(w, http.StatusForbidden, "administrator access required")
			return
		}
		handler(w, r)
	}
}
func mutating(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}
func currentUser(r *http.Request) domain.User {
	u, _ := r.Context().Value(userKey).(domain.User)
	return u
}
func currentSession(r *http.Request) domain.Session {
	s, _ := r.Context().Value(sessionKey).(domain.Session)
	return s
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "time": time.Now()})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username       string `json:"username"`
		Password       string `json:"password"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	// Argon2 only makes online brute force slow; the failure throttle makes
	// it stop. Both the claimed username and the source IP are locked out.
	ip := clientIP(r)
	if !s.verifyTurnstile(r.Context(), req.TurnstileToken, ip) {
		writeError(w, http.StatusBadRequest, "human verification failed")
		return
	}
	if s.loginThrottled(r.Context(), req.Username, ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed login attempts, please retry later")
		return
	}
	user, raw, session, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		s.noteLoginFailure(r.Context(), req.Username, ip)
		s.audit(r.Context(), domain.User{}, "auth.login", "user", req.Username, "failure", map[string]any{"ip": ip})
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.clearLoginFailures(r.Context(), req.Username)
	http.SetCookie(w, &http.Cookie{Name: "bce_session", Value: raw, Path: "/", HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	s.audit(r.Context(), user, "auth.login", "user", user.ID, "success", nil)
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrf_token": session.CSRFToken})
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie("bce_session")
	if cookie != nil {
		_ = s.auth.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "bce_session", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": currentUser(r), "csrf_token": currentSession(r).CSRFToken})
}

// updateMySettings lets a user edit their own retrieval preferences; currently
// just the per-retrieval output budget. 0 restores the server default.
func (s *Server) updateMySettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxOutputTokens *int `json:"max_output_tokens"`
	}
	if !decode(w, r, &req) {
		return
	}
	user, err := s.store.UserByID(r.Context(), currentUser(r).ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if req.MaxOutputTokens != nil {
		v := *req.MaxOutputTokens
		if v != 0 && (v < 1600 || v > 24000) {
			writeError(w, 400, "max output tokens must be between 1600 and 24000 (0 = default)")
			return
		}
		user.MaxOutputTokens = v
	}
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"user": user})
}

// generateACEToken (re)issues the caller's personal ACE/MCP token. Auth
// verifies the sha256 hash; the plaintext is also stored encrypted at rest so
// the console can re-display it. Regeneration invalidates the previous token.
func (s *Server) generateACEToken(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.UserByID(r.Context(), currentUser(r).ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	token := "bce_" + identity.NewToken(24)
	user.ACETokenHash = hashToken(token)
	user.ACEToken = token
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), user, "ace_token.generate", "user", user.ID, "success", nil)
	writeJSON(w, 200, map[string]any{"token": token})
}

// currentACEToken returns the caller's own token for console display; empty
// when none has been generated yet.
func (s *Server) currentACEToken(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.UserByID(r.Context(), currentUser(r).ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"token": user.ACEToken})
}

func (s *Server) myACEProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.indexer.ListACEProjects(r.Context(), currentUser(r).ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"projects": projects})
}

func (s *Server) deleteMyACEProject(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	name := r.PathValue("name")
	if err := s.indexer.PurgeACEProject(r.Context(), user.ID, name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, 404, "project not found")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	// The purge frees storage immediately; drop the cached total so the next
	// upload quota check sees it, and clear today's upload_bytes so the freed
	// space can be re-uploaded without tripping the daily volume backstop.
	s.dropStorageCache(r.Context(), user.ID)
	s.clearQuotaBlock(r.Context(), user.ID)
	_ = s.store.ClearQuotaUsageKind(r.Context(), user.ID, "upload_bytes")
	s.audit(r.Context(), user, "ace_project.delete", "ace_project", name, "success", nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) batchUpload(w http.ResponseWriter, r *http.Request) {
	// Fast refusal for users recently rejected on the storage ceiling: one
	// Redis EXISTS before the body is read or parsed. Abusive clients retry
	// a refused upload every few seconds for hours; without this, each retry
	// paid JSON-decoding megabytes just to be told no again.
	if user := currentUser(r); user.ID != "" && user.Role != domain.RoleAdmin && s.quotaBlocked(r.Context(), user.ID) {
		writeError(w, http.StatusRequestEntityTooLarge, "storage or daily upload limit exceeded; remove projects to free space, or wait for the daily reset at midnight")
		return
	}
	var req struct {
		Blobs []indexer.FileInput `json:"blobs"`
		// ProjectName (sent by bce-tool-rs) makes the project visible in the
		// console while the first upload is still running; the canonical
		// carrier for archival remains the retrieval request.
		ProjectName string `json:"project_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	// NOTE: the storage-abuse guard (abuseguard.go, 429 for hours-long
	// zero-retrieval upload streaks) is currently disabled by operator
	// decision; re-enable by restoring the abuseUploadAllowed gate here.
	// Measured in ciphertext units so the ceiling check compares like with
	// like against the database-side storage totals.
	var incoming int64
	for _, blob := range req.Blobs {
		incoming += store.EstimatedStoredSize(int64(len(blob.Content)))
	}
	if !s.enforceQuota(w, r, "upload", incoming) {
		return
	}
	names, err := s.indexer.UploadBlobs(r.Context(), req.Blobs)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Upload-pressure samples for the analytics ingest curve: raw plaintext
	// bytes accepted per request (pre-dedup — this measures inbound load, the
	// dedup savings show up as low CPU, not here).
	var rawBytes float64
	for _, blob := range req.Blobs {
		rawBytes += float64(len(blob.Content))
	}
	if len(req.Blobs) > 0 {
		now := time.Now()
		_ = s.store.RecordMetric(r.Context(), domain.MetricPoint{Scope: "ace", Name: "ingest_bytes", Value: rawBytes, Timestamp: now})
		_ = s.store.RecordMetric(r.Context(), domain.MetricPoint{Scope: "ace", Name: "ingest_blobs", Value: float64(len(req.Blobs)), Timestamp: now})
	}
	s.indexer.EnsureEmbeddingsAsync(names)
	if user := currentUser(r); user.ID != "" {
		s.indexer.EnsurePendingACEProject(r.Context(), user.ID, req.ProjectName)
	}
	s.recordACEUsage(r, "upload", int64(len(names)))
	s.bumpQuota(r, "upload")
	if user := currentUser(r); user.ID != "" && user.Role != domain.RoleAdmin {
		_ = s.store.AddQuotaUsage(r.Context(), user.ID, "upload_bytes", incoming)
	}
	s.noteStoredBytes(r, incoming)
	writeJSON(w, 200, map[string]any{"blob_names": names})
}
func (s *Server) aceSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InformationRequest string `json:"information_request"`
		Blobs              struct {
			CheckpointID any      `json:"checkpoint_id"`
			Added        []string `json:"added_blobs"`
			Deleted      []string `json:"deleted_blobs"`
		} `json:"blobs"`
		Dialog    []any `json:"dialog"`
		MaxOutput int   `json:"max_output_length"`
		Disable   bool  `json:"disable_codebase_retrieval"`
		Commit    bool  `json:"enable_commit_retrieval"`
		// ProjectName is a bce-tool-rs extension: the client's project folder
		// name, sent with every retrieval so the archived ACE project carries
		// (and follows renames of) the real workspace name. Absent from stock
		// ACE clients.
		ProjectName string `json:"project_name"`
	}
	if !decode(w, r, &req) {
		return
	}
	checkpointID, _ := req.Blobs.CheckpointID.(string)
	if req.Disable {
		writeJSON(w, 200, map[string]any{"formatted_retrieval": "", "checkpoint_id": checkpointID})
		return
	}
	if !s.enforceQuota(w, r, "retrieval", 0) {
		return
	}
	// A claimed checkpoint must belong to the caller: snapshots are shared
	// content-addressed objects, so without this check any personal-token
	// holder could mount another user's workspace by ID. Non-owned IDs get
	// the unknown-checkpoint treatment — the client re-uploads its manifest
	// and ownership is established through its own archived project. The
	// derived path (full blob-name list, no claimed ID) needs no check:
	// naming every blob already proves possession of the content.
	if user := currentUser(r); user.ID != "" && checkpointID != "" {
		owned, err := s.store.UserOwnsSnapshot(r.Context(), user.ID, checkpointID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if !owned {
			writeError(w, http.StatusGone, "unknown checkpoint")
			return
		}
	}
	checkpointID, blobs, err := s.indexer.ResolveCheckpoint(r.Context(), checkpointID, req.Blobs.Added, req.Blobs.Deleted)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusGone, "unknown checkpoint")
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	// Personal-token callers get their latest workspace state remembered so
	// the prompt enhancer can pull context from *their* checkpoint, and the
	// checkpoint archived as a named project for the console.
	if user := currentUser(r); user.ID != "" {
		_ = s.store.SetUserCheckpoint(r.Context(), user.ID, checkpointID)
		s.indexer.ArchiveACEProject(r.Context(), user.ID, checkpointID, req.ProjectName, len(req.Blobs.Added), len(req.Blobs.Deleted))
		s.noteRetrievalUse(r.Context(), user.ID)
	}
	// Same workspace content + same question + same output cap = same answer:
	// the checkpoint ID fingerprints the entire content, so a cache hit skips
	// the whole pipeline and any file change misses naturally. Usage and
	// quota accounting stay identical to a real search.
	maxOutput := userMaxOutput(r, req.MaxOutput)
	cacheKey := searchCacheKey(checkpointID, req.InformationRequest, maxOutput)
	if entry, ok := s.searchCacheGet(r.Context(), cacheKey); ok {
		// Cache hits skip the pipeline and thus record no latency sample; this
		// 1/0 sample is what the analytics hit-rate and QPS curves read.
		_ = s.store.RecordMetric(r.Context(), domain.MetricPoint{Scope: "ace", Name: "search_cache_hit", Value: 1, Timestamp: time.Now()})
		s.recordACEUsage(r, "retrieval", entry.Tokens)
		s.bumpQuota(r, "retrieval")
		writeJSON(w, 200, map[string]any{"formatted_retrieval": entry.Formatted, "checkpoint_id": checkpointID})
		return
	}
	_ = s.store.RecordMetric(r.Context(), domain.MetricPoint{Scope: "ace", Name: "search_cache_hit", Value: 0, Timestamp: time.Now()})
	// Concurrent identical requests (same content fingerprint + query + cap,
	// i.e. the same cache key) collapse into one pipeline run: clients fire
	// the same retrieval several times within a second, and running each copy
	// only makes all of them slower on a small host. The run is detached from
	// the leader's request context — its result is shared by followers whose
	// requests outlive the leader, and it must also survive to fill the cache
	// (same rationale as any cache-fill write). The cache put lives inside
	// the shared function so it happens exactly once per run.
	out, err, merged := s.searchGroup.Do(cacheKey, func() (any, error) {
		sctx := context.WithoutCancel(r.Context())
		result, err := s.indexer.SearchBlobNames(sctx, req.InformationRequest, blobs, maxOutput)
		if err != nil {
			return nil, err
		}
		// Degraded answers (semantic path absent, e.g. vectors still
		// backfilling) and ghost-blob answers must not get locked in for the
		// TTL: the next identical query would get the better answer the
		// pipeline can already produce by then.
		if !result.Degraded && result.MissingBlobs == 0 && result.FormattedRetrieval != "" {
			s.searchCachePut(sctx, cacheKey, searchCacheEntry{Formatted: result.FormattedRetrieval, Tokens: int64(result.TokenEstimate)})
		}
		return result, nil
	})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	result := out.(indexer.SearchResponse)
	// One attribution line per search: who, which workspace, how big, how it
	// went. Every latency investigation so far started by joining nginx
	// access logs against upload timestamps to guess whose search was slow —
	// this line answers that directly. merged=true marks a follower that
	// piggybacked on another request's run (duration shown is that run's).
	slog.Info("ace search",
		"user", currentUser(r).Username, "project", req.ProjectName, "files", len(blobs),
		"duration_ms", int(result.DurationMS), "degraded", result.Degraded, "missing", result.MissingBlobs,
		"merged", merged)
	s.recordACEUsage(r, "retrieval", int64(result.TokenEstimate))
	s.bumpQuota(r, "retrieval")
	writeJSON(w, 200, map[string]any{"formatted_retrieval": result.FormattedRetrieval, "checkpoint_id": checkpointID})
}

// promptEnhance implements the ACE /prompt-enhancer endpoint consumed by
// ace-tool-rs' enhance_prompt tool: the raw prompt arrives in text nodes and
// the enhanced prompt goes back as {"text": ...}. The client's model field is
// ignored — the configured local chat model is used instead.
func (s *Server) promptEnhance(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nodes []struct {
			ID       int `json:"id"`
			Type     int `json:"type"`
			TextNode struct {
				Content string `json:"content"`
			} `json:"text_node"`
		} `json:"nodes"`
		ChatHistory    []indexer.ChatTurn `json:"chat_history"`
		ConversationID string             `json:"conversation_id"`
		Model          string             `json:"model"`
		Mode           string             `json:"mode"`
	}
	if !decode(w, r, &req) {
		return
	}
	parts := make([]string, 0, len(req.Nodes))
	for _, node := range req.Nodes {
		if strings.TrimSpace(node.TextNode.Content) != "" {
			parts = append(parts, node.TextNode.Content)
		}
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n"))
	if prompt == "" {
		writeError(w, 400, "no prompt content in nodes")
		return
	}
	if !s.enforceQuota(w, r, "enhance", 0) {
		return
	}
	text, err := s.indexer.EnhancePrompt(r.Context(), currentUser(r).ID, prompt, req.ChatHistory)
	if err != nil {
		if strings.Contains(err.Error(), "not configured") {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}
		writeError(w, 502, err.Error())
		return
	}
	s.recordACEUsage(r, "enhance", int64(len(text)/4))
	s.bumpQuota(r, "enhance")
	s.noteRetrievalUse(r.Context(), currentUser(r).ID)
	writeJSON(w, 200, map[string]any{"text": text})
}

// myACEUsage reports the caller's own MCP/ACE traffic as a per-day,
// per-endpoint series for the trailing window (default 30 days); totals are
// derived client-side.
func (s *Server) myACEUsage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	daily, err := s.store.ACEUsageDaily(r.Context(), currentUser(r).ID, days)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"daily": daily})
}

// adminACEUsage reports per-user MCP/ACE usage aggregates plus the
// deployment-wide daily series for the trailing window (default 30 days);
// shared-token traffic shows up without a username.
func (s *Server) adminACEUsage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	rows, err := s.store.ListACEUsage(r.Context(), days)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	names := map[string]string{}
	if users, err := s.store.ListUsers(r.Context()); err == nil {
		for _, u := range users {
			names[u.ID] = u.Username
		}
	}
	for i := range rows {
		switch {
		case rows[i].UserID == "":
			rows[i].Username = "shared-token"
		case names[rows[i].UserID] != "":
			rows[i].Username = names[rows[i].UserID]
		default:
			rows[i].Username = rows[i].UserID
		}
	}
	daily, err := s.store.ACEUsageDaily(r.Context(), "", days)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"usage": rows, "daily": daily})
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.buildOverview(r.Context(), currentUser(r).ID, false)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, overview)
}
func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context(), currentUser(r).ID, false)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"repositories": repos})
}
func (s *Server) createRepository(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		RootPath string `json:"root_path"`
		Branch   string `json:"branch"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, 400, "name is required")
		return
	}
	now := time.Now()
	repo := domain.Repository{ID: identity.NewID("repo"), OwnerID: currentUser(r).ID, Name: req.Name, RootPath: req.RootPath, Branch: req.Branch, Status: domain.RepositoryHealthy, EmbeddingModel: "qwen3-embedding-0.6b", CreatedAt: now, UpdatedAt: now, LastSyncedAt: now}
	if repo.Branch == "" {
		repo.Branch = "main"
	}
	if err := s.store.CreateRepository(r.Context(), repo); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "repository.create", "repository", repo.ID, "success", nil)
	s.events.Publish("repository.updated", repo)
	writeJSON(w, 201, repo)
}
func (s *Server) repository(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	files, _ := s.store.RepositoryFiles(r.Context(), repo.ID)
	writeJSON(w, 200, map[string]any{"repository": repo, "files": files})
}
func (s *Server) repositoryMetrics(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	hours, _ := strconv.Atoi(r.URL.Query().Get("hours"))
	if hours <= 0 {
		hours = 24
	}
	points, err := s.store.Metrics(r.Context(), repo.ID, r.URL.Query().Get("name"), time.Now().Add(-time.Duration(hours)*time.Hour), 1000)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"metrics": points})
}
func (s *Server) repositoryJobs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	jobs, err := s.store.ListJobs(r.Context(), repo.OwnerID, false, 100)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	filtered := jobs[:0]
	for _, j := range jobs {
		if j.RepositoryID == repo.ID {
			filtered = append(filtered, j)
		}
	}
	writeJSON(w, 200, map[string]any{"jobs": filtered})
}
func (s *Server) syncRepository(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	if repo.Paused {
		writeError(w, 409, "repository is paused")
		return
	}
	var req indexer.SyncRequest
	if !decode(w, r, &req) {
		return
	}
	job, err := s.indexer.SyncRepository(r.Context(), repo, req)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "repository.sync", "repository", repo.ID, "success", map[string]any{"revision": job.Revision})
	writeJSON(w, 202, job)
}
func (s *Server) pauseRepository(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	var req struct {
		Paused bool `json:"paused"`
	}
	if !decode(w, r, &req) {
		return
	}
	repo.Paused = req.Paused
	if req.Paused {
		repo.Status = domain.RepositoryPaused
	} else {
		repo.Status = domain.RepositoryHealthy
	}
	repo.UpdatedAt = time.Now()
	if err := s.store.UpdateRepository(r.Context(), repo); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "repository.pause", "repository", repo.ID, "success", map[string]any{"paused": req.Paused})
	s.events.Publish("repository.updated", repo)
	writeJSON(w, 200, repo)
}
func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), currentUser(r).ID, false, 50)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}
func (s *Server) adminACEProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.indexer.ListAllACEProjects(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"projects": projects})
}

func (s *Server) inspectSearch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepositoryID string `json:"repository_id"`
		SnapshotID   string `json:"snapshot_id"`
		Query        string `json:"query"`
		MaxOutput    int    `json:"max_output_length"`
	}
	if !decode(w, r, &req) {
		return
	}
	// ACE project mode: the console inspects a checkpoint snapshot instead of
	// a registered repository. Ownership is checked against the caller's own
	// archived projects, mirroring authorizedRepo.
	if req.SnapshotID != "" {
		projects, err := s.indexer.ListACEProjects(r.Context(), currentUser(r).ID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		owned := false
		for _, p := range projects {
			if p.SnapshotID == req.SnapshotID {
				owned = true
				break
			}
		}
		if !owned {
			writeError(w, 404, "project not found")
			return
		}
		snap, err := s.store.SnapshotByID(r.Context(), req.SnapshotID)
		if err != nil {
			writeError(w, 404, "snapshot not found")
			return
		}
		result, err := s.indexer.SearchBlobNames(r.Context(), req.Query, snap.BlobNames, userMaxOutput(r, req.MaxOutput))
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, result)
		return
	}
	r2 := r.Clone(r.Context())
	r2.SetPathValue("id", req.RepositoryID)
	if _, ok := s.authorizedRepo(w, r2); !ok {
		return
	}
	result, err := s.indexer.SearchRepository(r.Context(), req.RepositoryID, req.Query, userMaxOutput(r, req.MaxOutput))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, result)
}

func (s *Server) listEvalCases(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	cases, err := s.store.ListEvalCases(r.Context(), repo.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"cases": cases})
}
func (s *Server) createEvalCase(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	var req struct {
		Query            string   `json:"query"`
		ExpectedPaths    []string `json:"expected_paths"`
		ExpectedKeywords []string `json:"expected_keywords"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Query) == "" || (len(req.ExpectedPaths) == 0 && len(req.ExpectedKeywords) == 0) {
		writeError(w, 400, "query and at least one expected path or keyword are required")
		return
	}
	c := domain.EvalCase{ID: identity.NewID("eval"), RepositoryID: repo.ID, Query: req.Query, ExpectedPaths: req.ExpectedPaths, ExpectedKeywords: req.ExpectedKeywords, CreatedAt: time.Now()}
	if err := s.store.CreateEvalCase(r.Context(), c); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "eval_case.create", "repository", repo.ID, "success", map[string]any{"case_id": c.ID})
	writeJSON(w, 201, c)
}
func (s *Server) deleteEvalCase(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	evalCase, err := s.store.EvalCaseByID(r.Context(), r.PathValue("caseId"))
	if err != nil || evalCase.RepositoryID != repo.ID {
		writeError(w, 404, "eval case not found")
		return
	}
	if err := s.store.DeleteEvalCase(r.Context(), evalCase.ID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) runEval(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.authorizedRepo(w, r)
	if !ok {
		return
	}
	cases, err := s.store.ListEvalCases(r.Context(), repo.ID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	type caseResult struct {
		Case          domain.EvalCase `json:"case"`
		PathHit       bool            `json:"path_hit"`
		MatchedPaths  []string        `json:"matched_paths"`
		KeywordRecall float64         `json:"keyword_recall"`
		DurationMS    float64         `json:"duration_ms"`
		Degraded      bool            `json:"degraded"`
	}
	results := make([]caseResult, 0, len(cases))
	pathCases, pathHits, keywordCases := 0, 0, 0
	keywordTotal, latencyTotal := 0.0, 0.0
	for _, c := range cases {
		search, err := s.indexer.SearchRepository(r.Context(), repo.ID, c.Query, 12000)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		item := caseResult{Case: c, MatchedPaths: []string{}, DurationMS: search.DurationMS, Degraded: search.Degraded}
		if len(c.ExpectedPaths) > 0 {
			pathCases++
			for _, expected := range c.ExpectedPaths {
				for _, hit := range search.Hits {
					if hit.Path == expected || strings.HasSuffix(hit.Path, expected) {
						item.MatchedPaths = append(item.MatchedPaths, hit.Path)
						break
					}
				}
			}
			item.PathHit = len(item.MatchedPaths) > 0
			if item.PathHit {
				pathHits++
			}
		}
		if len(c.ExpectedKeywords) > 0 {
			keywordCases++
			lowered := strings.ToLower(search.FormattedRetrieval)
			matched := 0
			for _, keyword := range c.ExpectedKeywords {
				if strings.Contains(lowered, strings.ToLower(keyword)) {
					matched++
				}
			}
			item.KeywordRecall = float64(matched) / float64(len(c.ExpectedKeywords))
			keywordTotal += item.KeywordRecall
		}
		latencyTotal += search.DurationMS
		results = append(results, item)
	}
	summary := map[string]any{"cases": len(cases), "path_cases": pathCases, "keyword_cases": keywordCases}
	if pathCases > 0 {
		summary["path_recall"] = float64(pathHits) / float64(pathCases)
	}
	if keywordCases > 0 {
		summary["keyword_recall"] = keywordTotal / float64(keywordCases)
	}
	if len(cases) > 0 {
		summary["avg_latency_ms"] = latencyTotal / float64(len(cases))
	}
	s.audit(r.Context(), currentUser(r), "eval.run", "repository", repo.ID, "success", map[string]any{"cases": len(cases)})
	writeJSON(w, 200, map[string]any{"results": results, "summary": summary})
}

func (s *Server) eventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 500, "streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	events, cancel := s.events.Subscribe()
	defer cancel()
	// Events are broadcast to every subscriber, and their payloads carry
	// other users' repository and job details. The console only uses them as
	// refresh signals, so non-admin subscribers get the type with an empty
	// payload.
	admin := currentUser(r).Role == domain.RoleAdmin
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event := <-events:
			raw := []byte("{}")
			if admin {
				raw, _ = json.Marshal(event.Data)
			}
			fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.ID, event.Type, raw)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) adminOverview(w http.ResponseWriter, r *http.Request) {
	o, err := s.buildOverview(r.Context(), "", true)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, o)
}
func (s *Server) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	// Project counts and storage aggregate ACE projects — the same rows the
	// console shows; the legacy repositories table stays empty under
	// MCP-only usage. Today's quota consumption rides along so the user list
	// can render usage against the deployment limits.
	projects, _ := s.indexer.ListAllACEProjects(r.Context())
	usage, _ := s.store.QuotaUsageTodayAll(r.Context())
	type row struct {
		domain.User
		Repositories   int   `json:"repositories"`
		StorageBytes   int64 `json:"storage_bytes"`
		RetrievalToday int64 `json:"retrieval_today"`
		EnhanceToday   int64 `json:"enhance_today"`
		UploadToday    int64 `json:"upload_today"`
	}
	out := make([]row, 0, len(users))
	for _, u := range users {
		x := row{User: u}
		for _, p := range projects {
			if p.OwnerID == u.ID {
				x.Repositories++
				x.StorageBytes += p.StorageBytes
			}
		}
		if used := usage[u.ID]; used != nil {
			x.RetrievalToday, x.EnhanceToday, x.UploadToday = used["retrieval"], used["enhance"], used["upload"]
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"users": out, "limits": s.quotaSettings(r.Context())})
}

// adminResetQuota zeroes the target user's counters for the current day,
// restoring today's allowance without touching usage statistics.
func (s *Server) adminResetQuota(w http.ResponseWriter, r *http.Request) {
	target, err := s.store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "user not found")
		return
	}
	if err := s.store.ResetQuotaUsage(r.Context(), target.ID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "user.quota_reset", "user", target.ID, "success", nil)
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) adminUpdateUser(w http.ResponseWriter, r *http.Request) {
	target, err := s.store.UserByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "user not found")
		return
	}
	var req struct {
		Disabled *bool       `json:"disabled"`
		Role     domain.Role `json:"role"`
		Password string      `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Disabled != nil {
		if *req.Disabled && target.Role == domain.RoleAdmin {
			writeError(w, http.StatusForbidden, "administrator accounts cannot be disabled")
			return
		}
		target.Disabled = *req.Disabled
	}
	if req.Role != "" {
		if req.Role != domain.RoleAdmin && req.Role != domain.RoleUser {
			writeError(w, 400, "invalid role")
			return
		}
		target.Role = req.Role
	}
	if req.Password != "" {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		target.PasswordHash = hash
	}
	if err := s.store.UpdateUser(r.Context(), target); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if target.Disabled || req.Password != "" {
		_ = s.store.DeleteUserSessions(r.Context(), target.ID)
	}
	s.audit(r.Context(), currentUser(r), "user.update", "user", target.ID, "success", map[string]any{"disabled": target.Disabled, "role": target.Role})
	writeJSON(w, 200, target)
}
func (s *Server) adminRepositories(w http.ResponseWriter, r *http.Request) {
	repos, err := s.store.ListRepositories(r.Context(), "", true)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"repositories": repos})
}
func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), "", true, 250)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"jobs": jobs})
}
func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.JobByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "job not found")
		return
	}
	repo, err := s.store.RepositoryByID(r.Context(), job.RepositoryID)
	if err != nil {
		writeError(w, 404, "repository not found")
		return
	}
	files, _ := s.store.RepositoryFiles(r.Context(), repo.ID)
	inputs := make([]indexer.FileInput, 0, len(files))
	for _, f := range files {
		inputs = append(inputs, indexer.FileInput{Path: f.Path, Content: f.Content})
	}
	newJob, err := s.indexer.SyncRepository(r.Context(), repo, indexer.SyncRequest{Files: inputs, Priority: "bulk"})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "index_job.retry", "index_job", job.ID, "success", map[string]any{"new_job_id": newJob.ID})
	writeJSON(w, 202, newJob)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.JobByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "job not found")
		return
	}
	if job.Status != domain.JobQueued {
		writeError(w, 409, "only queued jobs can be cancelled")
		return
	}
	job.Status = domain.JobCancelled
	job.Stage = "cancelled"
	job.CompletedAt = time.Now()
	_ = s.store.UpdateJob(r.Context(), job)
	s.audit(r.Context(), currentUser(r), "index_job.cancel", "index_job", job.ID, "success", nil)
	writeJSON(w, 200, job)
}
func (s *Server) infrastructure(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"settings": s.retrievalSettings(r.Context())})
}
func (s *Server) retrievalSettings(ctx context.Context) domain.RetrievalSettings {
	values, _ := s.store.GetSettings(ctx)
	get := func(key, def string) string {
		if v := values[key]; v != "" {
			return v
		}
		return def
	}
	getInt := func(key string, def int) int {
		v, _ := strconv.Atoi(values[key])
		if v > 0 {
			return v
		}
		return def
	}
	getBool := func(key string, def bool) bool {
		if values[key] == "" {
			return def
		}
		v, _ := strconv.ParseBool(values[key])
		return v
	}
	return domain.RetrievalSettings{
		EmbeddingProvider:   get("embedding_provider", s.config.EmbeddingProvider),
		EmbeddingURL:        get("embedding_url", s.config.EmbeddingURL),
		EmbeddingModel:      get("embedding_model", s.config.EmbeddingModel),
		EmbeddingDimensions: getInt("embedding_dimensions", s.config.EmbeddingDimensions),
		RerankerEnabled:     getBool("reranker_enabled", true),
		RerankerModel:       get("reranker_model", s.config.RerankerModel),
		RerankerTopK:        getInt("reranker_top_k", s.config.RerankerTopK),
		EnhancerProvider:    get("enhancer_provider", "openai-compatible"),
		EnhancerURL:         get("enhancer_url", s.config.EnhancerURL),
		EnhancerModel:       get("enhancer_model", s.config.EnhancerModel),
		SummaryModel:        get("summary_model", s.config.SummaryModel),
		SummaryBudget:       summaryBudgetValue(values["summary_budget"]),
		EmbeddingAPIKey:     values["embedding_api_key"],
		EnhancerAPIKey:      values["enhancer_api_key"],
		UpdatedAt:           time.Now(),
	}
}

// summaryBudgetValue resolves the stored summary_budget: explicit "0" is a
// valid value (rule-descriptions only), so the usual getInt "positive or
// default" pattern does not apply.
func summaryBudgetValue(raw string) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v >= 0 {
		return v
	}
	return indexer.DefaultSummaryBudget
}
func (s *Server) getQuotaSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.quotaSettings(r.Context()))
}
func (s *Server) updateQuotaSettings(w http.ResponseWriter, r *http.Request) {
	current := s.quotaSettings(r.Context())
	// Every limit is strictly positive, so zero doubles as "omitted" and the
	// pointer-shadow pattern other settings need does not apply here.
	var req domain.QuotaSettings
	if !decode(w, r, &req) {
		return
	}
	merge := func(dst *int, v, minV, maxV int, name string) bool {
		if v == 0 {
			return true
		}
		if v < minV || v > maxV {
			writeError(w, 400, fmt.Sprintf("%s must be between %d and %d", name, minV, maxV))
			return false
		}
		*dst = v
		return true
	}
	if !merge(&current.StorageLimitMB, req.StorageLimitMB, 1, 1048576, "storage limit (MB)") ||
		!merge(&current.DailyRetrieval, req.DailyRetrieval, 1, 1000000, "daily retrieval quota") ||
		!merge(&current.DailyEnhance, req.DailyEnhance, 1, 1000000, "daily enhance quota") ||
		!merge(&current.DailyUpload, req.DailyUpload, 1, 1000000, "daily upload quota") ||
		!merge(&current.RPMRetrieval, req.RPMRetrieval, 1, 100000, "retrieval RPM") ||
		!merge(&current.RPMUpload, req.RPMUpload, 1, 100000, "upload RPM") ||
		!merge(&current.RPMEnhance, req.RPMEnhance, 1, 100000, "enhance RPM") {
		return
	}
	values := map[string]string{
		"quota_storage_mb":      strconv.Itoa(current.StorageLimitMB),
		"quota_daily_retrieval": strconv.Itoa(current.DailyRetrieval),
		"quota_daily_enhance":   strconv.Itoa(current.DailyEnhance),
		"quota_daily_upload":    strconv.Itoa(current.DailyUpload),
		"rpm_retrieval":         strconv.Itoa(current.RPMRetrieval),
		"rpm_upload":            strconv.Itoa(current.RPMUpload),
		"rpm_enhance":           strconv.Itoa(current.RPMEnhance),
	}
	if err := s.store.SetSettings(r.Context(), values); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "quota_settings.update", "system", "quota", "success", map[string]any{"storage_mb": current.StorageLimitMB, "daily_retrieval": current.DailyRetrieval})
	writeJSON(w, 200, current)
}
func (s *Server) getRetrievalSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.retrievalSettings(r.Context()))
}
func (s *Server) updateRetrievalSettings(w http.ResponseWriter, r *http.Request) {
	current := s.retrievalSettings(r.Context())
	// summary_budget needs present/absent detection: 0 is a meaningful value
	// (LLM tier off), so the shadowing pointer distinguishes it from omitted.
	var req struct {
		domain.RetrievalSettings
		SummaryBudget *int `json:"summary_budget"`
		// The dedicated keys also need present/absent detection: submitting an
		// explicit empty string clears the override back to the fallback key.
		EmbeddingAPIKey *string `json:"embedding_api_key"`
		EnhancerAPIKey  *string `json:"enhancer_api_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.EmbeddingProvider != "" {
		current.EmbeddingProvider = req.EmbeddingProvider
	}
	if req.EmbeddingURL != "" {
		current.EmbeddingURL = req.EmbeddingURL
	}
	if req.EmbeddingModel != "" {
		current.EmbeddingModel = req.EmbeddingModel
	}
	if req.EmbeddingDimensions > 0 {
		current.EmbeddingDimensions = req.EmbeddingDimensions
	}
	current.RerankerEnabled = req.RerankerEnabled
	if req.RerankerModel != "" {
		current.RerankerModel = req.RerankerModel
	}
	if req.RerankerTopK > 0 {
		current.RerankerTopK = req.RerankerTopK
	}
	if req.EnhancerProvider != "" {
		current.EnhancerProvider = req.EnhancerProvider
	}
	if req.EnhancerURL != "" {
		current.EnhancerURL = req.EnhancerURL
	}
	if req.EnhancerModel != "" {
		current.EnhancerModel = req.EnhancerModel
	}
	if req.SummaryModel != "" {
		current.SummaryModel = req.SummaryModel
	}
	if req.SummaryBudget != nil {
		current.SummaryBudget = *req.SummaryBudget
	}
	if req.EmbeddingAPIKey != nil {
		current.EmbeddingAPIKey = *req.EmbeddingAPIKey
	}
	if req.EnhancerAPIKey != nil {
		current.EnhancerAPIKey = *req.EnhancerAPIKey
	}
	if current.EmbeddingDimensions < 128 || current.EmbeddingDimensions > 8192 || current.RerankerTopK < 1 || current.RerankerTopK > 200 {
		writeError(w, 400, "invalid model dimensions or reranker top-k")
		return
	}
	if current.SummaryBudget < 0 || current.SummaryBudget > 500 {
		writeError(w, 400, "summary budget must be between 0 and 500")
		return
	}
	values := map[string]string{"embedding_provider": current.EmbeddingProvider, "embedding_url": current.EmbeddingURL, "embedding_model": current.EmbeddingModel, "embedding_dimensions": strconv.Itoa(current.EmbeddingDimensions), "reranker_enabled": strconv.FormatBool(current.RerankerEnabled), "reranker_model": current.RerankerModel, "reranker_top_k": strconv.Itoa(current.RerankerTopK), "enhancer_provider": current.EnhancerProvider, "enhancer_url": current.EnhancerURL, "enhancer_model": current.EnhancerModel, "summary_model": current.SummaryModel, "summary_budget": strconv.Itoa(current.SummaryBudget), "embedding_api_key": current.EmbeddingAPIKey, "enhancer_api_key": current.EnhancerAPIKey}
	if err := s.store.SetSettings(r.Context(), values); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "retrieval_settings.update", "system", "retrieval", "success", map[string]any{"embedding_model": current.EmbeddingModel, "reranker_model": current.RerankerModel})
	current.UpdatedAt = time.Now()
	writeJSON(w, 200, current)
}
func (s *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListAudit(r.Context(), 250)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"events": events})
}

func (s *Server) authorizedRepo(w http.ResponseWriter, r *http.Request) (domain.Repository, bool) {
	repo, err := s.store.RepositoryByID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, 404, "repository not found")
		return domain.Repository{}, false
	}
	user := currentUser(r)
	if user.Role != domain.RoleAdmin && repo.OwnerID != user.ID {
		writeError(w, 403, "repository access denied")
		return domain.Repository{}, false
	}
	return repo, true
}
func (s *Server) buildOverview(ctx context.Context, owner string, all bool) (domain.Overview, error) {
	// The console works in ACE projects, so the overview aggregates the same
	// user_ace_projects rows the project pages show; the legacy repositories
	// table stays empty when every index arrives through an ACE client.
	var projects []domain.ACEProject
	var err error
	if all {
		projects, err = s.indexer.ListAllACEProjects(ctx)
	} else {
		projects, err = s.indexer.ListACEProjects(ctx, owner)
	}
	if err != nil {
		return domain.Overview{}, err
	}
	o := domain.Overview{}
	for _, p := range projects {
		o.Repositories++
		o.Files += p.FileCount
		o.Chunks += p.ChunkCount
		o.EmbeddedChunks += p.EmbeddedCount
		o.StorageBytes += p.StorageBytes
		if p.ChunkCount > 0 && p.EmbeddedCount < p.ChunkCount {
			o.Lagging++
		} else {
			o.Healthy++
		}
	}
	jobs, _ := s.store.ListJobs(ctx, owner, all, 500)
	for _, j := range jobs {
		if j.Status == domain.JobFailed {
			o.FailedJobs++
		}
	}
	metrics, _ := s.store.Metrics(ctx, "", "search_latency_ms", time.Now().Add(-24*time.Hour), 10000)
	values := make([]float64, 0, len(metrics))
	for _, m := range metrics {
		values = append(values, m.Value)
	}
	o.SearchP50MS = percentile(values, .5)
	o.SearchP95MS = percentile(values, .95)
	o.SearchP99MS = percentile(values, .99)
	if len(metrics) > 96 {
		metrics = metrics[len(metrics)-96:]
	}
	o.LatencyPoints = metrics
	if all {
		users, _ := s.store.ListUsers(ctx)
		o.ActiveUsers = len(users)
	}
	return o, nil
}
func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	index := int(float64(len(values)-1) * p)
	return values[index]
}
func (s *Server) audit(ctx context.Context, actor domain.User, action, targetType, targetID, result string, metadata map[string]any) {
	_ = s.store.CreateAudit(ctx, domain.AuditEvent{ID: identity.NewID("audit"), ActorID: actor.ID, Actor: actor.Username, Action: action, TargetType: targetType, TargetID: targetID, Result: result, Metadata: metadata, CreatedAt: time.Now()})
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}
