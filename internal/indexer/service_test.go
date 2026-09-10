package indexer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/events"
	"github.com/linqiu919/better-context-engine/internal/store"
)

func TestACEBlobHashIsPathAndContent(t *testing.T) {
	a := BlobName("a.go", "package a")
	b := BlobName("b.go", "package a")
	if a == b {
		t.Fatal("path must contribute to blob identity")
	}
	if a != BlobName("a.go", "package a") {
		t.Fatal("blob hash must be deterministic")
	}
}

func TestSyncAndSearchRepository(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	now := time.Now()
	user := domain.User{ID: "user_1", Username: "dev", Role: domain.RoleUser, CreatedAt: now, LastActiveAt: now}
	if err := st.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	repo := domain.Repository{ID: "repo_1", OwnerID: user.ID, Name: "sample", Branch: "main", Status: domain.RepositoryHealthy, EmbeddingModel: "test", CreatedAt: now, UpdatedAt: now, LastSyncedAt: now}
	if err := st.CreateRepository(ctx, repo); err != nil {
		t.Fatal(err)
	}
	service := New(st, events.New(), ModelDefaults{})
	if _, err := service.SyncRepository(ctx, repo, SyncRequest{Files: []FileInput{{Path: "internal/auth/service.go", Content: "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n"}}}); err != nil {
		t.Fatal(err)
	}
	result, err := service.SearchRepository(ctx, repo.ID, "where is authentication implemented", 8000)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 {
		t.Fatal("expected a search hit")
	}
	if result.Hits[0].Path != "internal/auth/service.go" {
		t.Fatalf("unexpected hit: %s", result.Hits[0].Path)
	}
	if !strings.Contains(result.FormattedRetrieval, "<codebase_context>") {
		t.Fatal("missing ACE context envelope")
	}
}
