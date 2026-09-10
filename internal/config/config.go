package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr                   string
	DatabaseURL            string
	EncryptionKey          string
	ACEToken               string
	CookieSecure           bool
	SessionTTL             time.Duration
	BootstrapAdminUsername string
	BootstrapAdminPassword string
	DemoData               bool
	EmbeddingProvider      string
	EmbeddingURL           string
	EmbeddingModel         string
	EmbeddingDimensions    int
	RerankerModel          string
	RerankerTopK           int
	EnhancerURL            string
	EnhancerModel          string
	SummaryModel           string
	ModelAPIKey            string
	RedisAddr              string
	RedisPassword          string
	// PublicURL is the externally reachable base URL (scheme+host), used to
	// build the OAuth redirect URI.
	PublicURL string
	// The self-service signup switch lives in the admin console's system
	// settings (default: open); SMTP below must be configured for the
	// verification mail to go out.
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPFrom            string
	// Turnstile protects login/registration; empty secret disables the check.
	TurnstileSiteKey   string
	TurnstileSecretKey string
	// LinuxDo Connect OAuth2; empty client id disables the entry point.
	LinuxDoClientID     string
	LinuxDoClientSecret string
}

// dotEnvPath returns the first .env that exists among: the working
// directory, the executable's directory, and its parent (covers a binary in
// bin/ started from inside bin/ with .env at the project root).
func dotEnvPath() string {
	candidates := []string{".env"}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates, filepath.Join(dir, ".env"), filepath.Join(dir, "..", ".env"))
	}
	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// loadDotEnv injects variables from the nearest .env file into the process
// environment before Load reads it. Keys already present in the real
// environment always win, so exported overrides and container deployments
// (compose passes env directly) behave exactly as before.
func loadDotEnv() {
	path := dotEnvPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
		value = strings.TrimSpace(value)
		// Strip one layer of matching quotes; values may legitimately
		// contain '#', so trailing comments are deliberately not parsed.
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}

func Load() Config {
	loadDotEnv()
	return Config{
		// 连接串/密钥/地址类沿用 BCE_ 前缀：这些通用名（DATABASE_URL 等）
		// 常被托管平台自动注入，真实环境变量优先于 .env，撞名会静默覆盖配置。
		Addr:                   env("BCE_ADDR", ":18181"),
		DatabaseURL:            os.Getenv("BCE_DATABASE_URL"),
		EncryptionKey:          os.Getenv("BCE_ENCRYPTION_KEY"),
		ACEToken:               env("ACE_TOKEN", "development-token"),
		CookieSecure:           envBool("COOKIE_SECURE", false),
		SessionTTL:             envDuration("SESSION_TTL", 24*time.Hour),
		BootstrapAdminUsername: env("BOOTSTRAP_ADMIN_USERNAME", "admin"),
		BootstrapAdminPassword: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
		DemoData:               envBool("DEMO_DATA", true),
		// Defaults target SiliconFlow (OpenAI-compatible cloud platform);
		// set MODEL_API_KEY or the three paths degrade to lexical-only.
		// Override the URLs/models via env to run fully local (ollama/vLLM).
		EmbeddingProvider:   env("EMBEDDING_PROVIDER", "openai-compatible"),
		EmbeddingURL:        env("EMBEDDING_URL", "https://api.siliconflow.cn/v1"),
		EmbeddingModel:      env("EMBEDDING_MODEL", "Qwen/Qwen3-Embedding-8B"),
		EmbeddingDimensions: envInt("EMBEDDING_DIMENSIONS", 4096),
		// The reranker shares the embedding provider/URL/key; only the model
		// name (and top-K) is its own.
		RerankerModel: env("RERANKER_MODEL", "Qwen/Qwen3-Reranker-8B"),
		RerankerTopK:  envInt("RERANKER_TOP_K", 24),
		EnhancerURL:   env("ENHANCER_URL", "https://api.siliconflow.cn/v1"),
		EnhancerModel: env("ENHANCER_MODEL", "Qwen/Qwen3.5-9B"),
		SummaryModel:  env("SUMMARY_MODEL", "Qwen/Qwen3-8B"),
		// Shared Bearer token for cloud model platforms (SiliconFlow etc.);
		// empty for local-only deployments.
		ModelAPIKey: os.Getenv("MODEL_API_KEY"),
		// Optional Redis for cross-instance rate-limit counters and caches;
		// empty keeps every counter in-process (single-binary default).
		RedisAddr:     os.Getenv("BCE_REDIS_ADDR"),
		RedisPassword: os.Getenv("BCE_REDIS_PASSWORD"),
		// Self-registration (email verification codes) and third-party login;
		// every entry point stays hidden until its variables are configured.
		PublicURL:           strings.TrimRight(os.Getenv("BCE_PUBLIC_URL"), "/"),
		SMTPHost:            env("SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:            envInt("SMTP_PORT", 587),
		SMTPUsername:        os.Getenv("SMTP_USERNAME"),
		SMTPPassword:        os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:            env("SMTP_FROM", os.Getenv("SMTP_USERNAME")),
		TurnstileSiteKey:    os.Getenv("TURNSTILE_SITE_KEY"),
		TurnstileSecretKey:  os.Getenv("TURNSTILE_SECRET_KEY"),
		LinuxDoClientID:     os.Getenv("LINUXDO_CLIENT_ID"),
		LinuxDoClientSecret: os.Getenv("LINUXDO_CLIENT_SECRET"),
	}
}

func envInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
