package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/linqiu919/better-context-engine/internal/domain"
	"github.com/linqiu919/better-context-engine/internal/identity"
	"github.com/linqiu919/better-context-engine/internal/store"
)

// Announcements: an admin publishes a Markdown notice from the console, and
// every signed-in user sees the latest one as a popup until they dismiss it.
// A dismissal remembers the announcement ID, so the popup returns only when a
// newer announcement is published. System settings (currently the
// registration switch) live here too — they share the admin settings page.

const (
	announcementTitleMax   = 200
	announcementContentMax = 16 << 10
	announcementListLimit  = 100
)

// getSystemSettings backs the "System settings" page: the effective
// registration switch plus whether SMTP is configured (without SMTP the email
// signup entry stays hidden even when the switch is on).
func (s *Server) getSystemSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"registration_enabled": s.registrationEnabled(r.Context()),
		"smtp_configured":      s.smtpConfigured(),
	})
}

func (s *Server) updateSystemSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistrationEnabled *bool `json:"registration_enabled"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.RegistrationEnabled != nil {
		if err := s.store.SetSettings(r.Context(), map[string]string{"registration_enabled": strconv.FormatBool(*req.RegistrationEnabled)}); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r.Context(), currentUser(r), "system_settings.update", "system", "registration", "success", map[string]any{"registration_enabled": *req.RegistrationEnabled})
	}
	s.getSystemSettings(w, r)
}

func (s *Server) adminListAnnouncements(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAnnouncements(r.Context(), announcementListLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcements": items})
}

func (s *Server) adminCreateAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if title == "" || content == "" {
		writeError(w, http.StatusBadRequest, "title and content are required")
		return
	}
	if len(title) > announcementTitleMax || len(content) > announcementContentMax {
		writeError(w, http.StatusBadRequest, "announcement is too long")
		return
	}
	user := currentUser(r)
	a := domain.Announcement{ID: identity.NewID("ann"), Title: title, Content: content, AuthorID: user.ID, Author: user.Username, CreatedAt: time.Now()}
	if err := s.store.CreateAnnouncement(r.Context(), a); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r.Context(), user, "announcement.publish", "announcement", a.ID, "success", map[string]any{"title": title})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) adminDeleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAnnouncement(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "announcement not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r.Context(), currentUser(r), "announcement.delete", "announcement", id, "success", nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// myAnnouncement returns the latest announcement unless the caller already
// dismissed it; null means the console has nothing to pop up. With ?latest=1
// (the topbar bell) the dismissal filter is skipped: dismissing only silences
// the sign-in popup, it must not make the notice unreadable afterwards.
func (s *Server) myAnnouncement(w http.ResponseWriter, r *http.Request) {
	latest, err := s.store.LatestAnnouncement(r.Context())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"announcement": nil})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("latest") != "1" {
		dismissed, err := s.store.DismissedAnnouncement(r.Context(), currentUser(r).ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if dismissed == latest.ID {
			writeJSON(w, http.StatusOK, map[string]any{"announcement": nil})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"announcement": latest})
}

// dismissAnnouncement records that the caller closed the given announcement;
// the popup stays away until a newer one is published.
func (s *Server) dismissAnnouncement(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.store.DismissAnnouncement(r.Context(), currentUser(r).ID, req.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
