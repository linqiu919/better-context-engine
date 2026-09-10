package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linqiu919/better-context-engine/internal/auth"
	"github.com/linqiu919/better-context-engine/internal/config"
	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/events"
	"github.com/linqiu919/better-context-engine/internal/indexer"
	"github.com/linqiu919/better-context-engine/internal/store"
)

func testServer(t *testing.T) http.Handler {
	t.Helper()
	st := store.NewMemory()
	hash, err := auth.HashPassword("test-administrator-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.CreateUser(context.Background(), domain.User{ID: "admin", Username: "admin", Role: domain.RoleAdmin, PasswordHash: hash, CreatedAt: now, LastActiveAt: now}); err != nil {
		t.Fatal(err)
	}
	broker := events.New()
	cfg := config.Config{ACEToken: "ace-token", SessionTTL: time.Hour}
	return New(cfg, st, auth.New(st, time.Hour), indexer.New(st, broker, indexer.ModelDefaults{}), broker, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
}
func TestACEUploadAndSearch(t *testing.T) {
	handler := testServer(t)
	upload := httptest.NewRequest(http.MethodPost, "/batch-upload", strings.NewReader(`{"blobs":[{"path":"auth.go","content":"package auth\nfunc Login() {}"}]}`))
	upload.Header.Set("Authorization", "Bearer ace-token")
	upload.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, upload)
	if rec.Code != 200 {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	var uploaded struct {
		Names []string `json:"blob_names"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &uploaded)
	payload, _ := json.Marshal(map[string]any{"information_request": "login", "blobs": map[string]any{"added_blobs": uploaded.Names, "deleted_blobs": []string{}}, "dialog": []any{}, "max_output_length": 0, "disable_codebase_retrieval": false, "enable_commit_retrieval": false})
	search := httptest.NewRequest(http.MethodPost, "/agents/codebase-retrieval", bytes.NewReader(payload))
	search.Header.Set("Authorization", "Bearer ace-token")
	search.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, search)
	if rec.Code != 200 {
		t.Fatalf("search: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "auth.go") {
		t.Fatalf("expected formatted retrieval, got %s", rec.Body.String())
	}
}
