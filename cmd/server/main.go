package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/linqiu919/better-context-engine/internal/auth"
	"github.com/linqiu919/better-context-engine/internal/config"
	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/events"
	"github.com/linqiu919/better-context-engine/internal/httpapi"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/indexer"
	"github.com/linqiu919/better-context-engine/internal/store"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	ctx := context.Background()
	var st store.Store
	var err error
	if cfg.DatabaseURL != "" {
		st, err = store.NewPostgres(ctx, cfg.DatabaseURL, cfg.EncryptionKey)
		if err != nil {
			logger.Error("connect database", "error", err)
			os.Exit(1)
		}
	} else {
		st = store.NewMemory()
		logger.Warn("using in-memory store; set BCE_DATABASE_URL for persistence")
	}
	defer st.Close()
	password := cfg.BootstrapAdminPassword
	if password == "" {
		password = identity.NewToken(15)
		logger.Warn("generated one-time bootstrap administrator password", "username", cfg.BootstrapAdminUsername, "password", password)
	}
	admin := ensureUser(ctx, st, cfg.BootstrapAdminUsername, password, domain.RoleAdmin)
	broker := events.New()
	idx := indexer.New(st, broker, indexer.ModelDefaults{EmbeddingProvider: cfg.EmbeddingProvider, EmbeddingURL: cfg.EmbeddingURL, EmbeddingModel: cfg.EmbeddingModel, EmbeddingDimensions: cfg.EmbeddingDimensions, RerankerModel: cfg.RerankerModel, RerankerTopK: cfg.RerankerTopK, EnhancerURL: cfg.EnhancerURL, EnhancerModel: cfg.EnhancerModel, SummaryModel: cfg.SummaryModel, ModelAPIKey: cfg.ModelAPIKey})
	if cfg.DemoData {
		seedDemo(ctx, st, idx, admin)
	}
	idx.StartEmbeddingBackfill()
	// Expired session rows are already invisible to reads; this loop keeps
	// them from accumulating in the table forever.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := st.DeleteExpiredSessions(ctx); err != nil {
				logger.Warn("expired session cleanup", "error", err)
			}
		}
	}()
	authService := auth.New(st, cfg.SessionTTL)
	handler := httpapi.New(cfg, st, authService, idx, broker, logger)
	server := &http.Server{Addr: cfg.Addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	go func() {
		logger.Info("better context engine listening", "address", cfg.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdown)
}

func ensureUser(ctx context.Context, st store.Store, username, password string, role domain.Role) domain.User {
	if user, err := st.UserByUsername(ctx, username); err == nil {
		return user
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		panic(err)
	}
	now := time.Now()
	user := domain.User{ID: identity.NewID("user"), Username: username, Role: role, PasswordHash: hash, CreatedAt: now, LastActiveAt: now}
	if err := st.CreateUser(ctx, user); err != nil {
		panic(err)
	}
	return user
}
func seedDemo(ctx context.Context, st store.Store, idx *indexer.Service, admin domain.User) {
	users, _ := st.ListUsers(ctx)
	if len(users) > 1 {
		return
	}
	developer := ensureUser(ctx, st, "developer", "developer-demo-password", domain.RoleUser)
	now := time.Now()
	repos := []domain.Repository{{ID: identity.NewID("repo"), OwnerID: admin.ID, Name: "better-context-engine", RootPath: "/workspace/better-context-engine", Branch: "main", BaseSnapshot: "4f2a9c1", Status: domain.RepositoryHealthy, EmbeddingModel: "qwen3-embedding-0.6b", CreatedAt: now, UpdatedAt: now, LastSyncedAt: now}, {ID: identity.NewID("repo"), OwnerID: developer.ID, Name: "payments-service", RootPath: "/workspace/payments-service", Branch: "feature/telemetry", BaseSnapshot: "a91ce20", Status: domain.RepositoryHealthy, EmbeddingModel: "qwen3-embedding-0.6b", CreatedAt: now, UpdatedAt: now, LastSyncedAt: now}}
	for _, repo := range repos {
		_ = st.CreateRepository(ctx, repo)
		_, _ = idx.SyncRepository(ctx, repo, indexer.SyncRequest{Branch: repo.Branch, BaseSnapshot: repo.BaseSnapshot, Priority: "realtime", Files: []indexer.FileInput{{Path: "internal/server/server.go", Content: "package server\n\nfunc Start() error { return nil }\n"}, {Path: "internal/auth/service.go", Content: "package auth\n\nfunc Authenticate(token string) bool { return token != \"\" }\n"}, {Path: "README.md", Content: "# " + repo.Name + "\n\nA service indexed by Better Context Engine.\n"}}})
	}
}
