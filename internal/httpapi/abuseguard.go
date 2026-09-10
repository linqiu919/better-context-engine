package httpapi

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Abuse guard for storage-style upload patterns. The service is a retrieval
// engine, but its content-addressed upload endpoint doubles as free encrypted
// backup for anyone willing to script it: observed abusers upload for hours
// every day (max out the daily byte quota) without ever running a retrieval,
// burning ingest CPU and embedding fees for content nobody searches. The
// tell is behavioral and simple: real users retrieve; hoarders don't.
//
// Rule: uploads in abuseWindowHours consecutive hour buckets, each bucket
// with at least abuseMinPerHour requests, and zero retrievals/enhancements in
// the same window → uploads answer 429 for abuseBlockTTL. Any successful
// retrieval lifts the block immediately, so a legitimate user can always
// free themselves by simply using the product; admins are exempt.
//
// State lives in Redis (hour-bucket counters, a recent-use marker and the
// block flag survive restarts and are shared across instances), falling back
// to in-process maps under quotaMu like every other counter in this package.
const (
	abuseWindowHours = 5
	abuseMinPerHour  = 3
	abuseBlockTTL    = 5 * time.Hour
	// Bucket/use-marker lifetime: one hour longer than the evaluation window
	// so the oldest inspected bucket is still alive when evaluated.
	abuseStateTTL = (abuseWindowHours + 1) * time.Hour
)

const abuseBlockMessage = "uploads temporarily paused: this account has been uploading continuously for hours without a single retrieval, which looks like bulk storage rather than code search. The pause lifts automatically within 5 hours - or immediately after your next codebase retrieval."

// abuseEntry is the in-process fallback state for one user.
type abuseEntry struct {
	hours        map[int64]int
	lastUse      time.Time
	blockedUntil time.Time
}

func abuseHour(now time.Time) int64 { return now.Unix() / 3600 }

func abuseBucketKey(userID string, hour int64) string {
	return fmt.Sprintf(redisKeyPrefix+"abuse:up:%s:%d", userID, hour)
}

// abuseUploadAllowed records one upload request for the user and reports
// whether the request may proceed. Called for authenticated non-admin
// uploads only.
func (s *Server) abuseUploadAllowed(ctx context.Context, userID string) bool {
	now := time.Now()
	hour := abuseHour(now)
	if s.redisUsable() {
		if allowed, handled := s.abuseUploadAllowedRedis(ctx, userID, hour); handled {
			return allowed
		}
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	entry := s.abuse[userID]
	if entry == nil {
		entry = &abuseEntry{hours: map[int64]int{}}
		s.abuse[userID] = entry
	}
	if now.Before(entry.blockedUntil) {
		return false
	}
	entry.hours[hour]++
	for h := range entry.hours {
		if h <= hour-abuseWindowHours-1 {
			delete(entry.hours, h)
		}
	}
	if entry.hours[hour] >= abuseMinPerHour && now.Sub(entry.lastUse) > abuseStateTTL {
		tripped := true
		for h := hour - abuseWindowHours + 1; h <= hour; h++ {
			if entry.hours[h] < abuseMinPerHour {
				tripped = false
				break
			}
		}
		if tripped {
			entry.blockedUntil = now.Add(abuseBlockTTL)
			return false
		}
	}
	return true
}

// abuseUploadAllowedRedis is the Redis-backed path; the second return is
// false when Redis failed and the in-process fallback should decide instead.
func (s *Server) abuseUploadAllowedRedis(ctx context.Context, userID string, hour int64) (bool, bool) {
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	blockKey := redisKeyPrefix + "abuse:block:" + userID
	pipe := s.redis.Pipeline()
	blocked := pipe.Exists(rctx, blockKey)
	count := pipe.Incr(rctx, abuseBucketKey(userID, hour))
	pipe.Expire(rctx, abuseBucketKey(userID, hour), abuseStateTTL)
	if _, err := pipe.Exec(rctx); err != nil {
		s.noteRedisError("abuse-guard", err)
		return false, false
	}
	if blocked.Val() > 0 {
		return false, true
	}
	// Evaluating the window costs an extra round trip, so it only runs once
	// the current bucket alone qualifies.
	if count.Val() < abuseMinPerHour {
		return true, true
	}
	keys := []string{redisKeyPrefix + "abuse:use:" + userID}
	for h := hour - abuseWindowHours + 1; h < hour; h++ {
		keys = append(keys, abuseBucketKey(userID, h))
	}
	values, err := s.redis.MGet(rctx, keys...).Result()
	if err != nil {
		s.noteRedisError("abuse-guard", err)
		return false, false
	}
	if values[0] != nil { // recent retrieval/enhance: never block
		return true, true
	}
	for _, v := range values[1:] {
		raw, ok := v.(string)
		if !ok {
			return true, true // an empty bucket breaks the streak
		}
		if n, err := strconv.Atoi(raw); err != nil || n < abuseMinPerHour {
			return true, true
		}
	}
	if err := s.redis.Set(rctx, blockKey, "1", abuseBlockTTL).Err(); err != nil {
		s.noteRedisError("abuse-guard", err)
	}
	return false, true
}

// noteRetrievalUse marks the user as a real retrieval consumer and lifts any
// active upload block: a hoarder who starts searching stops being a hoarder.
func (s *Server) noteRetrievalUse(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		pipe := s.redis.Pipeline()
		pipe.Set(rctx, redisKeyPrefix+"abuse:use:"+userID, "1", abuseStateTTL)
		pipe.Del(rctx, redisKeyPrefix+"abuse:block:"+userID)
		if _, err := pipe.Exec(rctx); err != nil {
			s.noteRedisError("abuse-guard", err)
		}
	}
	s.quotaMu.Lock()
	if entry := s.abuse[userID]; entry != nil {
		entry.lastUse = time.Now()
		entry.blockedUntil = time.Time{}
	} else {
		s.abuse[userID] = &abuseEntry{hours: map[int64]int{}, lastUse: time.Now()}
	}
	s.quotaMu.Unlock()
}
