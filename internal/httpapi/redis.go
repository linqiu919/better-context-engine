package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is an optional accelerator for the counters that must survive
// restarts and stay correct across instances: the RPM windows, the login
// failure throttle and the per-user storage cache. Every operation is capped
// by redisTimeout and fails open — on any error the caller falls back to the
// in-process implementation for redisCooldown, so a degraded Redis can slow
// nothing down and block nobody.
const (
	redisTimeout    = 500 * time.Millisecond
	redisCooldown   = 30 * time.Second
	storageCacheTTL = time.Minute

	loginFailLimit   = 5
	loginFailLockout = 5 * time.Minute
)

// redisKeyPrefix namespaces every key this service writes — the Redis
// instance is shared with other applications on the host. Every key literal
// in this package must be built on it.
const redisKeyPrefix = "better-context-engine:"

// failEntry is the in-process fallback for one login-failure counter.
type failEntry struct {
	count   int
	expires time.Time
}

func newRedisClient(addr, password string) *redis.Client {
	if addr == "" {
		return nil
	}
	return redis.NewClient(&redis.Options{Addr: addr, Password: password, DialTimeout: redisTimeout, ReadTimeout: redisTimeout, WriteTimeout: redisTimeout})
}

func (s *Server) redisUsable() bool {
	if s.redis == nil {
		return false
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	return time.Now().After(s.redisDownUntil)
}

func (s *Server) noteRedisError(op string, err error) {
	s.quotaMu.Lock()
	s.redisDownUntil = time.Now().Add(redisCooldown)
	s.quotaMu.Unlock()
	s.logger.Warn("redis unavailable, using in-process counters", "op", op, "error", err)
}

func redisCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, redisTimeout)
}

// allowRPMRedis consumes one slot of the caller's fixed 60-second window in
// Redis. The second return reports whether Redis handled the check at all.
func (s *Server) allowRPMRedis(ctx context.Context, userID, kind string, limit int) (bool, bool) {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	key := fmt.Sprintf(redisKeyPrefix+"rpm:%s:%s:%d", userID, kind, time.Now().Unix()/60)
	pipe := s.redis.Pipeline()
	count := pipe.Incr(rctx, key)
	pipe.Expire(rctx, key, 2*time.Minute)
	if _, err := pipe.Exec(rctx); err != nil {
		s.noteRedisError("rpm", err)
		return false, false
	}
	return count.Val() <= int64(limit), true
}

// clientIP prefers the first X-Forwarded-For hop (the deployment fronts the
// server with a reverse proxy, so RemoteAddr is usually the proxy itself).
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		first, _, _ := strings.Cut(fwd, ",")
		return strings.TrimSpace(first)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func loginFailKeys(username, ip string) [2]string {
	return [2]string{redisKeyPrefix + "loginfail:u:" + strings.ToLower(username), redisKeyPrefix + "loginfail:ip:" + ip}
}

// loginThrottled reports whether the username or the source IP accumulated
// loginFailLimit recent failures. Argon2 alone only slows online brute force;
// this locks it out for loginFailLockout per dimension.
func (s *Server) loginThrottled(ctx context.Context, username, ip string) bool {
	keys := loginFailKeys(username, ip)
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		values, err := s.redis.MGet(rctx, keys[0], keys[1]).Result()
		if err == nil {
			for _, v := range values {
				if raw, ok := v.(string); ok {
					if n, err := strconv.Atoi(raw); err == nil && n >= loginFailLimit {
						return true
					}
				}
			}
			return false
		}
		s.noteRedisError("login-throttle", err)
	}
	now := time.Now()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	for _, key := range keys {
		entry, ok := s.loginFails[key]
		if !ok {
			continue
		}
		if now.After(entry.expires) {
			delete(s.loginFails, key)
			continue
		}
		if entry.count >= loginFailLimit {
			return true
		}
	}
	return false
}

// noteLoginFailure bumps both counters; each failure re-arms the lockout
// window, so an ongoing attack stays locked until it pauses.
func (s *Server) noteLoginFailure(ctx context.Context, username, ip string) {
	keys := loginFailKeys(username, ip)
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		pipe := s.redis.Pipeline()
		for _, key := range keys {
			pipe.Incr(rctx, key)
			pipe.Expire(rctx, key, loginFailLockout)
		}
		_, err := pipe.Exec(rctx)
		if err == nil {
			return
		}
		s.noteRedisError("login-throttle", err)
	}
	now := time.Now()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	for _, key := range keys {
		entry := s.loginFails[key]
		if now.After(entry.expires) {
			entry.count = 0
		}
		entry.count++
		entry.expires = now.Add(loginFailLockout)
		s.loginFails[key] = entry
	}
}

// clearLoginFailures forgives the username counter after a successful login.
// The IP counter is left to expire on its own so a compromised account cannot
// be used to reset an IP-wide lockout.
func (s *Server) clearLoginFailures(ctx context.Context, username string) {
	key := loginFailKeys(username, "")[0]
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		if err := s.redis.Del(rctx, key).Err(); err != nil {
			s.noteRedisError("login-throttle", err)
		}
	}
	s.quotaMu.Lock()
	delete(s.loginFails, key)
	s.quotaMu.Unlock()
}

// storageBytesRedis serves the per-user storage total from Redis, computing
// it from the database on a miss. The second return reports whether Redis
// handled the lookup.
func (s *Server) storageBytesRedis(ctx context.Context, userID string) (int64, bool) {
	key := redisKeyPrefix + "storage:" + userID
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	total, err := s.redis.Get(rctx, key).Int64()
	if err == nil {
		return total, true
	}
	if !errors.Is(err, redis.Nil) {
		s.noteRedisError("storage-cache", err)
		return 0, false
	}
	total = s.computeStorageBytes(ctx, userID)
	if err := s.redis.Set(rctx, key, total, storageCacheTTL).Err(); err != nil {
		s.noteRedisError("storage-cache", err)
	}
	return total, true
}

// storageBumpScript adds to the cached total only while the entry exists,
// and refreshes the TTL on every bump: during a continuous upload session the
// accumulated estimate must stay alive (before the first retrieval archives
// the snapshot, the database truth is 0, so an expiry mid-session would reset
// the ceiling check). Once uploads pause for a TTL the database truth wins.
var storageBumpScript = redis.NewScript(`if redis.call('EXISTS', KEYS[1]) == 1 then local v = redis.call('INCRBY', KEYS[1], ARGV[1]) redis.call('PEXPIRE', KEYS[1], ARGV[2]) return v end return 0`)

func (s *Server) noteStoredBytesRedis(ctx context.Context, userID string, n int64) bool {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	if err := storageBumpScript.Run(rctx, s.redis, []string{redisKeyPrefix + "storage:" + userID}, n, storageCacheTTL.Milliseconds()).Err(); err != nil {
		s.noteRedisError("storage-cache", err)
		return false
	}
	return true
}

// quotaBlockTTL bounds how long an over-limit user is refused from the cheap
// block flag before the full quota math runs again — it self-heals after an
// admin raises the limits or the daily counter resets at midnight.
const quotaBlockTTL = 10 * time.Minute

func quotaBlockKey(userID string) string { return redisKeyPrefix + "quota:block:" + userID }

// quotaBlocked reports whether the user was recently refused for exceeding
// the upload/storage ceiling. Checked before the upload body is even read:
// observed clients retry a rejected upload every few seconds for hours, and
// parsing megabytes of JSON just to refuse again is pure wasted CPU. This is
// a Redis-only accelerator — without Redis the full check simply runs.
func (s *Server) quotaBlocked(ctx context.Context, userID string) bool {
	if !s.redisUsable() {
		return false
	}
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	n, err := s.redis.Exists(rctx, quotaBlockKey(userID)).Result()
	if err != nil {
		s.noteRedisError("quota-block", err)
		return false
	}
	return n > 0
}

// setQuotaBlock arms the fast-refusal flag after a storage-ceiling refusal.
func (s *Server) setQuotaBlock(ctx context.Context, userID string) {
	if !s.redisUsable() {
		return
	}
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	if err := s.redis.Set(rctx, quotaBlockKey(userID), "1", quotaBlockTTL).Err(); err != nil {
		s.noteRedisError("quota-block", err)
	}
}

// clearQuotaBlock lifts the fast-refusal flag immediately — called when the
// user frees space by deleting a project, so they are not stuck waiting out
// the TTL after fixing the very thing they were refused for.
func (s *Server) clearQuotaBlock(ctx context.Context, userID string) {
	if !s.redisUsable() {
		return
	}
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	if err := s.redis.Del(rctx, quotaBlockKey(userID)).Err(); err != nil {
		s.noteRedisError("quota-block", err)
	}
}

// dropStorageCache invalidates the user's cached storage total everywhere —
// called after a project deletion so the freed space is visible immediately.
func (s *Server) dropStorageCache(ctx context.Context, userID string) {
	s.quotaMu.Lock()
	delete(s.storageCache, userID)
	s.quotaMu.Unlock()
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		if err := s.redis.Del(rctx, redisKeyPrefix+"storage:"+userID).Err(); err != nil {
			s.noteRedisError("storage-cache", err)
		}
	}
}
