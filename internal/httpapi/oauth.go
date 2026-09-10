package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/store"
)

// LinuxDo Connect (https://connect.linux.do) third-party login: a standard
// OAuth2 authorization-code flow. Accounts are linked by the immutable forum
// user id; first login auto-creates a passwordless local account (empty
// PasswordHash cannot pass VerifyPassword, so password login stays closed).
const (
	linuxdoAuthorizeURL = "https://connect.linux.do/oauth2/authorize"
	linuxdoTokenURL     = "https://connect.linux.do/oauth2/token"
	linuxdoUserURL      = "https://connect.linux.do/api/user"
	// linuxdoMinTrustLevel keeps freshly created forum accounts out; level 1
	// requires basic participation.
	linuxdoMinTrustLevel = 1
	oauthStateCookie     = "bce_oauth_state"
)

func (s *Server) linuxdoRedirectURI() string {
	return s.config.PublicURL + "/api/v1/auth/linuxdo/callback"
}

// linuxdoStart sends the browser to the LinuxDo authorization page with a
// random state pinned in a short-lived cookie against login CSRF.
func (s *Server) linuxdoStart(w http.ResponseWriter, r *http.Request) {
	if !s.linuxdoConfigured() {
		writeError(w, http.StatusNotFound, "linuxdo login is not configured")
		return
	}
	state := identity.NewToken(24)
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: state, Path: "/api/v1/auth/linuxdo", HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	q := url.Values{"client_id": {s.config.LinuxDoClientID}, "response_type": {"code"}, "redirect_uri": {s.linuxdoRedirectURI()}, "state": {state}}
	http.Redirect(w, r, linuxdoAuthorizeURL+"?"+q.Encode(), http.StatusFound)
}

// linuxdoCallback finishes the flow: state check, code exchange, profile
// fetch, then log-in-or-create. Failures land back on the login page with a
// human-readable reason in the oauth_error query parameter.
func (s *Server) linuxdoCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		http.Redirect(w, r, "/?oauth_error="+url.QueryEscape(msg)+"#login", http.StatusFound)
	}
	if !s.linuxdoConfigured() {
		fail("linuxdo login is not configured")
		return
	}
	stateCookie, err := r.Cookie(oauthStateCookie)
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Path: "/api/v1/auth/linuxdo", MaxAge: -1, HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode})
	if err != nil || stateCookie.Value == "" || subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(r.URL.Query().Get("state"))) != 1 {
		fail("login state validation failed, please retry")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail("authorization was cancelled")
		return
	}
	token, err := s.linuxdoExchange(r.Context(), code)
	if err != nil {
		s.logger.Warn("linuxdo token exchange", "error", err)
		fail("failed to reach LinuxDo, please retry")
		return
	}
	profile, err := s.linuxdoFetchUser(r.Context(), token)
	if err != nil {
		s.logger.Warn("linuxdo profile fetch", "error", err)
		fail("failed to reach LinuxDo, please retry")
		return
	}
	if !profile.Active || profile.Silenced {
		fail("this LinuxDo account is not in good standing")
		return
	}
	if profile.TrustLevel < linuxdoMinTrustLevel {
		fail(fmt.Sprintf("LinuxDo trust level %d or higher is required", linuxdoMinTrustLevel))
		return
	}
	user, err := s.store.UserByLinuxDoID(r.Context(), profile.ID)
	switch {
	case err == nil:
		if user.Disabled {
			fail("this account has been disabled")
			return
		}
		// Refresh the mirrored forum profile on every login.
		user.LinuxDoUsername = profile.Username
		user.TrustLevel = profile.TrustLevel
		user.AvatarURL = profile.avatarURL()
	case errors.Is(err, store.ErrNotFound):
		// First-time OAuth sign-in creates an account, which is registration
		// in disguise — the same switch gates it. Existing linked accounts
		// are unaffected and keep signing in above. SMTP is deliberately not
		// required here (no verification mail is involved).
		if !s.registrationEnabled(r.Context()) {
			fail("registration is disabled")
			return
		}
		now := time.Now()
		user = domain.User{ID: identity.NewID("user"), Username: s.uniqueUsername(r.Context(), profile.Username), Role: domain.RoleUser, AvatarURL: profile.avatarURL(), LinuxDoID: profile.ID, LinuxDoUsername: profile.Username, TrustLevel: profile.TrustLevel, CreatedAt: now, LastActiveAt: now}
		if err := s.store.CreateUser(r.Context(), user); err != nil {
			s.logger.Warn("linuxdo account creation", "error", err)
			fail("failed to create the account, please retry")
			return
		}
		s.audit(r.Context(), user, "auth.register", "user", user.ID, "success", map[string]any{"provider": "linuxdo", "linuxdo_id": profile.ID})
	default:
		fail("failed to look up the account, please retry")
		return
	}
	raw, session, err := s.auth.StartSession(r.Context(), user)
	if err != nil {
		fail("failed to start the session, please retry")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "bce_session", Value: raw, Path: "/", HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	s.audit(r.Context(), user, "auth.login", "user", user.ID, "success", map[string]any{"provider": "linuxdo"})
	http.Redirect(w, r, "/", http.StatusFound)
}

type linuxdoProfile struct {
	ID             int64  `json:"id"`
	Username       string `json:"username"`
	Name           string `json:"name"`
	AvatarTemplate string `json:"avatar_template"`
	Active         bool   `json:"active"`
	TrustLevel     int    `json:"trust_level"`
	Silenced       bool   `json:"silenced"`
}

// avatarURL resolves the Discourse avatar template ({size} placeholder,
// possibly host-relative) into a concrete image URL.
func (p linuxdoProfile) avatarURL() string {
	if p.AvatarTemplate == "" {
		return ""
	}
	avatar := strings.ReplaceAll(p.AvatarTemplate, "{size}", "120")
	if !strings.HasPrefix(avatar, "http") {
		avatar = "https://linux.do" + avatar
	}
	return avatar
}

// linuxdoExchange trades the authorization code for an access token; LinuxDo
// expects the client credentials as HTTP Basic auth.
func (s *Server) linuxdoExchange(ctx context.Context, code string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {s.linuxdoRedirectURI()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linuxdoTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.config.LinuxDoClientID, s.config.LinuxDoClientSecret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint returned %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	return out.AccessToken, nil
}

func (s *Server) linuxdoFetchUser(ctx context.Context, token string) (linuxdoProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, linuxdoUserURL, nil)
	if err != nil {
		return linuxdoProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return linuxdoProfile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return linuxdoProfile{}, fmt.Errorf("user endpoint returned %d", resp.StatusCode)
	}
	var profile linuxdoProfile
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		return linuxdoProfile{}, err
	}
	if profile.ID == 0 {
		return linuxdoProfile{}, errors.New("profile missing user id")
	}
	return profile, nil
}

var usernameSanitizeRE = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// uniqueUsername derives a free local username from the forum handle,
// appending a numeric suffix on collision.
func (s *Server) uniqueUsername(ctx context.Context, base string) string {
	base = usernameSanitizeRE.ReplaceAllString(base, "")
	if len(base) > 28 {
		base = base[:28]
	}
	if len(base) < 3 {
		base = "ld-" + identity.NewToken(3)
	}
	if _, err := s.store.UserByUsername(ctx, base); err != nil {
		return base
	}
	for i := 2; i <= 50; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, err := s.store.UserByUsername(ctx, candidate); err != nil {
			return candidate
		}
	}
	return base + "-" + identity.NewToken(4)
}
