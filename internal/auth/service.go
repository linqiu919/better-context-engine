package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/store"
	"golang.org/x/crypto/argon2"
)

var ErrInvalidCredentials = errors.New("invalid credentials")

type Service struct {
	store store.Store
	ttl   time.Duration
}

func New(st store.Store, ttl time.Duration) *Service { return &Service{store: st, ttl: ttl} }

func HashPassword(password string) (string, error) {
	if len(password) < 10 {
		return "", errors.New("password must contain at least 10 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash keeps the unknown-username path as expensive as verifying a real
// account, so login response timing cannot be used to enumerate usernames.
var dummyHash, _ = HashPassword(identity.NewToken(16))

func (s *Service) Login(ctx context.Context, username, password string) (domain.User, string, domain.Session, error) {
	user, err := s.store.UserByUsername(ctx, username)
	if err != nil {
		VerifyPassword(dummyHash, password)
		return domain.User{}, "", domain.Session{}, ErrInvalidCredentials
	}
	if !VerifyPassword(user.PasswordHash, password) || user.Disabled {
		return domain.User{}, "", domain.Session{}, ErrInvalidCredentials
	}
	raw, session, err := s.StartSession(ctx, user)
	if err != nil {
		return domain.User{}, "", domain.Session{}, err
	}
	return user, raw, session, nil
}

// StartSession issues a session for an already-authenticated user (password
// login, registration, OAuth callback) and bumps last activity.
func (s *Service) StartSession(ctx context.Context, user domain.User) (string, domain.Session, error) {
	raw := identity.NewToken(32)
	session := domain.Session{TokenHash: TokenHash(raw), CSRFToken: identity.NewToken(24), UserID: user.ID, ExpiresAt: time.Now().Add(s.ttl)}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return "", domain.Session{}, err
	}
	user.LastActiveAt = time.Now()
	_ = s.store.UpdateUser(ctx, user)
	return raw, session, nil
}

func (s *Service) Authenticate(ctx context.Context, raw string) (domain.User, domain.Session, error) {
	if raw == "" {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	session, err := s.store.SessionByTokenHash(ctx, TokenHash(raw))
	if err != nil {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	user, err := s.store.UserByID(ctx, session.UserID)
	if err != nil || user.Disabled {
		return domain.User{}, domain.Session{}, ErrInvalidCredentials
	}
	return user, session, nil
}

func (s *Service) Logout(ctx context.Context, raw string) error {
	return s.store.DeleteSession(ctx, TokenHash(raw))
}

func TokenHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
