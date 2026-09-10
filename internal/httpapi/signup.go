package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/auth"
	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/version"
	"github.com/redis/go-redis/v9"
)

// Self-service registration: the browser asks for an email verification code,
// the code arrives via SMTP, and the signup form exchanges username + code +
// password for an account and a session. Every step behind Turnstile when a
// secret is configured. Codes live in Redis when available and fall back to
// the in-process map with identical semantics (same fail-open contract as the
// other counters).
const (
	regCodeTTL      = 10 * time.Minute
	regCodeCooldown = time.Minute
	regCodeMaxTries = 5
	turnstileVerify = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
)

var usernameRE = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,32}$`)

// allowedEmailDomains is the registration allowlist: only mainstream mail
// providers, so disposable-mail domains cannot be used to farm accounts for
// free quota. Served to the console via authConfig — the register page
// renders it as a domain dropdown, so keep the order user-friendly.
var allowedEmailDomains = []string{"gmail.com", "163.com", "qq.com"}

func emailDomainAllowed(email string) bool {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false
	}
	domain := email[at+1:]
	for _, d := range allowedEmailDomains {
		if domain == d {
			return true
		}
	}
	return false
}

// regCodeEntry is the in-process fallback for one pending verification code.
type regCodeEntry struct {
	code          string
	expires       time.Time
	tries         int
	cooldownUntil time.Time
}

// registrationEnabled resolves the self-service signup switch from the admin
// console's system settings; deployments that never touched the settings page
// default to open. It also gates first-time LinuxDo account creation (see
// oauth.go).
func (s *Server) registrationEnabled(ctx context.Context) bool {
	values, _ := s.store.GetSettings(ctx)
	if raw := values["registration_enabled"]; raw != "" {
		v, err := strconv.ParseBool(raw)
		return err == nil && v
	}
	return true
}

// registrationOpen requires both the switch and a working mail configuration;
// the console hides the signup entry when this is false.
func (s *Server) registrationOpen(ctx context.Context) bool {
	return s.registrationEnabled(ctx) && s.smtpConfigured()
}

func (s *Server) smtpConfigured() bool {
	return s.config.SMTPHost != "" && s.config.SMTPUsername != "" && s.config.SMTPPassword != ""
}

func (s *Server) linuxdoConfigured() bool {
	return s.config.LinuxDoClientID != "" && s.config.LinuxDoClientSecret != "" && s.config.PublicURL != ""
}

// authConfig tells the (unauthenticated) console which entry points exist so
// it can hide what is not configured. The Turnstile site key is public by
// design — it is embedded in every page that renders the widget.
func (s *Server) authConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"registration_enabled": s.registrationOpen(r.Context()),
		"turnstile_site_key":   s.config.TurnstileSiteKey,
		"linuxdo_enabled":      s.linuxdoConfigured(),
		"email_domains":        allowedEmailDomains,
		"version":              version.Version,
	})
}

// verifyTurnstile checks a Cloudflare Turnstile response token. An empty
// configured secret disables the feature entirely; once configured, failures
// (including Cloudflare being unreachable) reject the request — this guards
// login and signup, so failing open would defeat its purpose.
func (s *Server) verifyTurnstile(ctx context.Context, token, ip string) bool {
	if s.config.TurnstileSecretKey == "" {
		return true
	}
	if token == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	form := url.Values{"secret": {s.config.TurnstileSecretKey}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileVerify, strings.NewReader(form.Encode()))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.logger.Warn("turnstile verification request failed", "error", err)
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	return out.Success
}

func newEmailCode() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%06d", binary.BigEndian.Uint32(b[:])%1000000)
}

// normalizeEmail lowercases and validates a bare address (no display name).
func normalizeEmail(raw string) (string, bool) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > 254 {
		return "", false
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return "", false
	}
	return email, true
}

// regCooldownArm reserves the per-email send slot; false means a code went
// out less than regCodeCooldown ago.
func (s *Server) regCooldownArm(ctx context.Context, email string) bool {
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		ok, err := s.redis.SetNX(rctx, redisKeyPrefix+"regcd:"+email, "1", regCodeCooldown).Result()
		if err == nil {
			return ok
		}
		s.noteRedisError("register-code", err)
	}
	now := time.Now()
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	entry := s.regCodes[email]
	if now.Before(entry.cooldownUntil) {
		return false
	}
	entry.cooldownUntil = now.Add(regCodeCooldown)
	s.regCodes[email] = entry
	return true
}

// regCooldownClear releases the send slot after a failed delivery so the user
// does not have to wait a full minute to retry a mistyped mail server hiccup.
func (s *Server) regCooldownClear(ctx context.Context, email string) {
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		if err := s.redis.Del(rctx, redisKeyPrefix+"regcd:"+email).Err(); err != nil {
			s.noteRedisError("register-code", err)
		}
	}
	s.quotaMu.Lock()
	entry := s.regCodes[email]
	entry.cooldownUntil = time.Time{}
	s.regCodes[email] = entry
	s.quotaMu.Unlock()
}

func (s *Server) storeRegCode(ctx context.Context, email, code string) {
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		pipe := s.redis.Pipeline()
		pipe.Set(rctx, redisKeyPrefix+"regcode:"+email, code, regCodeTTL)
		pipe.Del(rctx, redisKeyPrefix+"regtry:"+email)
		_, err := pipe.Exec(rctx)
		if err == nil {
			return
		}
		s.noteRedisError("register-code", err)
	}
	s.quotaMu.Lock()
	entry := s.regCodes[email]
	entry.code = code
	entry.expires = time.Now().Add(regCodeTTL)
	entry.tries = 0
	s.regCodes[email] = entry
	s.quotaMu.Unlock()
}

// checkRegCode consumes one verification attempt. The code is deleted on
// success and after regCodeMaxTries wrong guesses, so a 6-digit code cannot
// be brute-forced within its lifetime.
func (s *Server) checkRegCode(ctx context.Context, email, code string) bool {
	if code == "" {
		return false
	}
	if s.redisUsable() {
		rctx, cancel := redisCtx(ctx)
		defer cancel()
		want, err := s.redis.Get(rctx, redisKeyPrefix+"regcode:"+email).Result()
		if err != nil {
			if !errors.Is(err, redis.Nil) {
				s.noteRedisError("register-code", err)
				return s.checkRegCodeLocal(email, code)
			}
			return false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			s.redis.Del(rctx, redisKeyPrefix+"regcode:"+email, redisKeyPrefix+"regtry:"+email)
			return true
		}
		tries, err := s.redis.Incr(rctx, redisKeyPrefix+"regtry:"+email).Result()
		if err == nil {
			s.redis.Expire(rctx, redisKeyPrefix+"regtry:"+email, regCodeTTL)
			if tries >= regCodeMaxTries {
				s.redis.Del(rctx, redisKeyPrefix+"regcode:"+email, redisKeyPrefix+"regtry:"+email)
			}
		}
		return false
	}
	return s.checkRegCodeLocal(email, code)
}

func (s *Server) checkRegCodeLocal(email, code string) bool {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	entry, ok := s.regCodes[email]
	if !ok || entry.code == "" || time.Now().After(entry.expires) {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(entry.code), []byte(code)) == 1 {
		delete(s.regCodes, email)
		return true
	}
	entry.tries++
	if entry.tries >= regCodeMaxTries {
		delete(s.regCodes, email)
		return false
	}
	s.regCodes[email] = entry
	return false
}

// Mail failure stages, so handlers can map an SMTP error onto a user-facing
// message: recipient problems are the visitor's to fix, auth/sender problems
// are the operator's, everything else is transient ("retry later").
var (
	errMailConnect   = errors.New("mail connect")
	errMailConfig    = errors.New("mail config")
	errMailRecipient = errors.New("mail recipient")
)

// sendMail delivers one small transactional mail through the configured SMTP
// relay (Gmail with an app password by default). Port 465 speaks TLS from the
// first byte (SMTPS); every other port starts plain and upgrades via
// STARTTLS when the server offers it (587). A non-empty htmlBody upgrades the
// message to multipart/alternative so clients that block HTML still read the
// plain-text part.
func (s *Server) sendMail(to, subject, textBody, htmlBody string) error {
	addr := fmt.Sprintf("%s:%d", s.config.SMTPHost, s.config.SMTPPort)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if s.config.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: s.config.SMTPHost})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", errMailConnect, err)
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	client, err := smtp.NewClient(conn, s.config.SMTPHost)
	if err != nil {
		conn.Close()
		return fmt.Errorf("%w: %v", errMailConnect, err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.config.SMTPHost}); err != nil {
			return fmt.Errorf("%w: %v", errMailConnect, err)
		}
	}
	if err := client.Auth(smtp.PlainAuth("", s.config.SMTPUsername, s.config.SMTPPassword, s.config.SMTPHost)); err != nil {
		return fmt.Errorf("%w: %v", errMailConfig, err)
	}
	if err := client.Mail(s.config.SMTPFrom); err != nil {
		return fmt.Errorf("%w: %v", errMailConfig, err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("%w: %v", errMailRecipient, err)
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	var msg strings.Builder
	msg.WriteString("From: Better Context Engine <" + s.config.SMTPFrom + ">\r\n")
	msg.WriteString("To: <" + to + ">\r\n")
	msg.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	msg.WriteString("MIME-Version: 1.0\r\n")
	if htmlBody == "" {
		msg.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		msg.WriteString(textBody)
	} else {
		const boundary = "bce-mail-boundary"
		msg.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
		msg.WriteString("--" + boundary + "\r\n")
		msg.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		msg.WriteString(textBody)
		msg.WriteString("\r\n--" + boundary + "\r\n")
		msg.WriteString("Content-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
		msg.WriteString(htmlBody)
		msg.WriteString("\r\n--" + boundary + "--\r\n")
	}
	if _, err := writer.Write([]byte(msg.String())); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// registerCodeMailHTML renders the verification mail in the console's visual
// language ("warm paper" light palette: #faf9f7 page, white card on #e8e6e1
// hairlines, Georgia display heading, #2f6fed brand accent). Email clients
// ignore <style> blocks, so everything is inline table layout; colors are
// hard-coded to the light theme because mails have no theme switch.
func registerCodeMailHTML(code string) string {
	return `<!doctype html>
<html lang="zh">
<head><meta charset="utf-8"></head>
<body style="margin:0;padding:0;background:#faf9f7;">
<div style="display:none;max-height:0;overflow:hidden;">您的 BCE 注册验证码：` + code + `（10 分钟内有效）</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#faf9f7;">
<tr><td align="center" style="padding:48px 16px;">
  <table role="presentation" width="440" cellpadding="0" cellspacing="0" style="max-width:440px;width:100%;background:#ffffff;border:1px solid #e8e6e1;border-radius:10px;">
  <tr><td style="padding:28px 36px 0;">
    <table role="presentation" cellpadding="0" cellspacing="0"><tr>
      <td style="width:4px;height:18px;background:#2f6fed;border-radius:2px;font-size:0;line-height:0;">&nbsp;</td>
      <td style="padding-left:10px;font-family:'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;font-size:14px;font-weight:600;letter-spacing:.2px;color:#171512;">Better Context Engine</td>
    </tr></table>
  </td></tr>
  <tr><td style="padding:26px 36px 0;font-family:Georgia,'Songti SC','Noto Serif SC',serif;font-size:22px;font-weight:600;color:#171512;">注册验证码</td></tr>
  <tr><td style="padding:12px 36px 0;font-family:'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;font-size:14px;line-height:22px;color:#5e5a53;">您好，您正在注册 Better Context Engine 账号。请在注册页面输入以下验证码完成邮箱验证：</td></tr>
  <tr><td style="padding:20px 36px 0;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
      <td align="center" style="background:#f4f3f0;border:1px solid #e8e6e1;border-radius:6px;padding:18px 0;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:30px;font-weight:600;letter-spacing:10px;color:#171512;">` + code + `</td>
    </tr></table>
  </td></tr>
  <tr><td style="padding:14px 36px 0;font-family:'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;font-size:12px;line-height:19px;color:#6f6a62;">验证码 10 分钟内有效，请勿泄露给任何人。</td></tr>
  <tr><td style="padding:24px 36px 28px;">
    <table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>
      <td style="border-top:1px solid #e8e6e1;padding-top:16px;font-family:'Helvetica Neue',Arial,'PingFang SC','Microsoft YaHei',sans-serif;font-size:12px;line-height:19px;color:#6f6a62;">如非本人操作，请忽略本邮件。<br>Better Context Engine · 上下文检索引擎</td>
    </tr></table>
  </td></tr>
  </table>
</td></tr>
</table>
</body>
</html>`
}

// sendRegisterCode mails a 6-digit code to the address a visitor wants to
// register with. Turnstile plus a per-email cooldown keep this from becoming
// a mail cannon; the address must not belong to an existing account.
func (s *Server) sendRegisterCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email          string `json:"email"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.registrationOpen(r.Context()) {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if !emailDomainAllowed(email) {
		writeError(w, http.StatusBadRequest, "email domain not supported")
		return
	}
	if !s.verifyTurnstile(r.Context(), req.TurnstileToken, clientIP(r)) {
		writeError(w, http.StatusBadRequest, "human verification failed")
		return
	}
	if _, err := s.store.UserByEmail(r.Context(), email); err == nil {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if !s.regCooldownArm(r.Context(), email) {
		writeError(w, http.StatusTooManyRequests, "code already sent, please retry in a minute")
		return
	}
	code := newEmailCode()
	subject := "BCE 注册验证码"
	text := fmt.Sprintf("您好，\r\n\r\n您正在注册 Better Context Engine 账号，验证码为：\r\n\r\n    %s\r\n\r\n验证码 10 分钟内有效。如非本人操作，请忽略本邮件。\r\n", code)
	if err := s.sendMail(email, subject, text, registerCodeMailHTML(code)); err != nil {
		s.regCooldownClear(r.Context(), email)
		s.logger.Warn("send verification mail", "error", err)
		// Surface the failure category, not the raw SMTP error (which may
		// leak host/credential details into the browser).
		msg, status := "failed to send verification email, please retry later", http.StatusBadGateway
		switch {
		case errors.Is(err, errMailRecipient):
			msg, status = "email address rejected by the mail server, please check it", http.StatusBadRequest
		case errors.Is(err, errMailConfig):
			msg = "mail service misconfigured, please contact the administrator"
		}
		writeError(w, status, msg)
		return
	}
	s.storeRegCode(r.Context(), email, code)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// register exchanges username + verified email code + password for a fresh
// account and logs it in. The code is only consumed after the cheap
// uniqueness checks so a taken username does not burn a valid code.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username        string `json:"username"`
		Email           string `json:"email"`
		Code            string `json:"code"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
		TurnstileToken  string `json:"turnstile_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !s.registrationOpen(r.Context()) {
		writeError(w, http.StatusForbidden, "registration is disabled")
		return
	}
	username := strings.TrimSpace(req.Username)
	if !usernameRE.MatchString(username) {
		writeError(w, http.StatusBadRequest, "username must be 3-32 characters (letters, digits, . _ -)")
		return
	}
	email, ok := normalizeEmail(req.Email)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid email address")
		return
	}
	if !emailDomainAllowed(email) {
		writeError(w, http.StatusBadRequest, "email domain not supported")
		return
	}
	if req.Password != req.ConfirmPassword {
		writeError(w, http.StatusBadRequest, "passwords do not match")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !s.verifyTurnstile(r.Context(), req.TurnstileToken, clientIP(r)) {
		writeError(w, http.StatusBadRequest, "human verification failed")
		return
	}
	if _, err := s.store.UserByUsername(r.Context(), username); err == nil {
		writeError(w, http.StatusConflict, "username already taken")
		return
	}
	if _, err := s.store.UserByEmail(r.Context(), email); err == nil {
		writeError(w, http.StatusConflict, "email already registered")
		return
	}
	if !s.checkRegCode(r.Context(), email, strings.TrimSpace(req.Code)) {
		writeError(w, http.StatusBadRequest, "invalid or expired verification code")
		return
	}
	now := time.Now()
	user := domain.User{ID: identity.NewID("user"), Username: username, Role: domain.RoleUser, PasswordHash: hash, Email: email, CreatedAt: now, LastActiveAt: now}
	if err := s.store.CreateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusConflict, "username or email already taken")
		return
	}
	raw, session, err := s.auth.StartSession(r.Context(), user)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "bce_session", Value: raw, Path: "/", HttpOnly: true, Secure: s.config.CookieSecure, SameSite: http.SameSiteLaxMode, Expires: session.ExpiresAt})
	s.audit(r.Context(), user, "auth.register", "user", user.ID, "success", map[string]any{"ip": clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "csrf_token": session.CSRFToken})
}
