package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/linqiu919/better-context-engine/internal/store"
)

// Error codes are the machine-readable half of every error body. They are
// part of the API contract (bce-tool, cmd/mcp and agent prompts branch on
// them), so existing values must never be renamed; add new ones instead.
const (
	errBadRequest     = "bad_request"
	errUnauthorized   = "unauthorized"
	errForbidden      = "forbidden"
	errNotFound       = "not_found"
	errConflict       = "conflict"
	errGone           = "gone"
	errTooLarge       = "too_large"
	errRateLimited    = "rate_limited"
	errInternal       = "internal"
	errNotImplemented = "not_implemented"
	errUpstream       = "upstream_error"

	// Specific states an ACE client can act on.
	errBodyTooLarge         = "body_too_large"          // split the upload
	errQuotaExhausted       = "quota_exhausted"         // daily allowance; Retry-After = next Beijing midnight
	errStorageLimit         = "storage_limit"           // delete projects or wait for the daily reset
	errCheckpointUnknown    = "checkpoint_unknown"      // drop local state, re-upload everything
	errEnhancerUnconfigured = "enhancer_not_configured" // deployment state, retrying will not help
	errInvalidToken         = "invalid_token"
	errRepositoryPaused     = "repository_paused"
)

func statusErrCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return errBadRequest
	case http.StatusUnauthorized:
		return errUnauthorized
	case http.StatusForbidden:
		return errForbidden
	case http.StatusNotFound:
		return errNotFound
	case http.StatusConflict:
		return errConflict
	case http.StatusGone:
		return errGone
	case http.StatusRequestEntityTooLarge:
		return errTooLarge
	case http.StatusTooManyRequests:
		return errRateLimited
	case http.StatusNotImplemented:
		return errNotImplemented
	case http.StatusBadGateway:
		return errUpstream
	}
	if status >= 500 {
		return errInternal
	}
	return errBadRequest
}

// setRetryAfter tells the client when retrying can succeed, rounded up to
// whole seconds (the header has no finer unit) and never below one.
func setRetryAfter(w http.ResponseWriter, d time.Duration) {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}

// untilQuotaReset is the wait until the daily counters roll over at
// midnight Beijing time (the same boundary the store's day keys use).
func untilQuotaReset(now time.Time) time.Duration {
	local := now.In(store.QuotaZone)
	next := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, store.QuotaZone)
	return next.Sub(now)
}

// rpmRetryAfter is the wait until the caller's RPM window rolls over: Redis
// buckets are wall-clock minutes (unix/60), the in-process window starts at
// its first request.
func (s *Server) rpmRetryAfter(now time.Time) time.Duration {
	if s.redisUsable() {
		return now.Truncate(time.Minute).Add(time.Minute).Sub(now)
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	return s.rpmWindow.Add(time.Minute).Sub(now)
}
