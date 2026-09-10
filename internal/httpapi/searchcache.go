package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Retrieval cache. The checkpoint ID is derived from the sorted blob-name set
// and blob names are content-addressed, so the ID is a fingerprint of the
// entire workspace content: the same (checkpoint, query, output cap) triple
// deterministically describes the same retrieval. Any file change produces a
// different checkpoint ID, so stale entries become unreachable instead of
// needing invalidation. The TTL only bounds staleness against things outside
// the key — model or retrieval-settings changes in the admin console.
//
// Redis-only by design: a cache is an optional accelerator, so there is no
// in-process fallback; without Redis every search just runs the pipeline.
const searchCacheTTL = 6 * time.Hour

type searchCacheEntry struct {
	Formatted string `json:"f"`
	Tokens    int64  `json:"t"`
}

func searchCacheKey(checkpointID, query string, maxOutput int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", checkpointID, maxOutput, query)))
	return redisKeyPrefix + "scache:" + hex.EncodeToString(h[:])
}

func (s *Server) searchCacheGet(ctx context.Context, key string) (searchCacheEntry, bool) {
	if !s.redisUsable() {
		return searchCacheEntry{}, false
	}
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	raw, err := s.redis.Get(rctx, key).Result()
	if err != nil {
		if err != redis.Nil {
			s.noteRedisError("search-cache", err)
		}
		return searchCacheEntry{}, false
	}
	var entry searchCacheEntry
	if json.Unmarshal([]byte(raw), &entry) != nil || entry.Formatted == "" {
		return searchCacheEntry{}, false
	}
	return entry, true
}

func (s *Server) searchCachePut(ctx context.Context, key string, entry searchCacheEntry) {
	if !s.redisUsable() {
		return
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	rctx, cancel := redisCtx(ctx)
	defer cancel()
	if err := s.redis.Set(rctx, key, raw, searchCacheTTL).Err(); err != nil {
		s.noteRedisError("search-cache", err)
	}
}
