// Package server wires HTTP routes, middleware and the embedded UI.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/strikesecurity/stepshell/internal/auth"
	"github.com/strikesecurity/stepshell/internal/config"
	"github.com/strikesecurity/stepshell/internal/kube"
	"github.com/strikesecurity/stepshell/internal/session"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	cfg      *config.Config
	log      *slog.Logger
	sessions *session.Manager
	auth     *auth.Authenticator // nil in dev/none mode
	factory  *kube.Factory
	mux      *http.ServeMux
}

// New builds a Server and registers routes. auth may be nil when
// cfg.AuthMode == "none".
func New(cfg *config.Config, log *slog.Logger, sm *session.Manager, a *auth.Authenticator, f *kube.Factory) *Server {
	s := &Server{cfg: cfg, log: log, sessions: sm, auth: a, factory: f, mux: http.NewServeMux()}
	s.routes()
	return s
}

// Handler returns the root http.Handler.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Health.
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	s.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })

	// Auth.
	if s.auth != nil {
		s.mux.HandleFunc("GET /auth/login", s.auth.Login)
		s.mux.HandleFunc("GET /auth/callback", s.auth.Callback)
		s.mux.HandleFunc("POST /auth/logout", s.auth.Logout)
	} else {
		s.mux.HandleFunc("GET /auth/login", s.devLogin)
		s.mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, _ *http.Request) {
			s.sessions.Clear(w)
			w.WriteHeader(204)
		})
	}

	// JSON API (require session + CSRF for mutations).
	api := func(pattern string, h http.HandlerFunc, mutating bool) {
		var wrapped http.Handler = h
		if mutating {
			wrapped = s.requireCSRF(wrapped)
		}
		s.mux.Handle(pattern, s.requireSession(wrapped))
	}
	api("GET /api/v1/me", s.handleMe, false)
	api("GET /api/v1/namespaces", s.handleNamespaces, false)
	api("GET /api/v1/namespaces/{ns}/pods", s.handleListPods, false)
	api("GET /api/v1/namespaces/{ns}/pods/{pod}", s.handleGetPod, false)
	api("POST /api/v1/namespaces/{ns}/pods/{pod}/debug", s.handleAttachDebug, true)
	api("POST /api/v1/namespaces/{ns}/pods/{pod}/release", s.handleRelease, true)

	if s.cfg.Argo.Enabled {
		api("GET /api/v1/namespaces/{ns}/workflows", s.handleListWorkflows, false)
		api("GET /api/v1/namespaces/{ns}/workflows/{name}", s.handleGetWorkflow, false)
		api("POST /api/v1/namespaces/{ns}/workflows/{name}/debug", s.handleWorkflowDebug, true)
	}

	// WebSocket exec (session required; origin checked in the handler).
	s.mux.Handle("GET /ws/exec", s.requireSession(http.HandlerFunc(s.handleExecWS)))

	// UI (embedded static assets + SPA fallback).
	s.mux.Handle("/", s.uiHandler())
}

// devLogin issues a session for the configured dev identity (auth mode none).
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	id := session.Identity{User: s.cfg.DevUser, Groups: s.cfg.DevGroups, Email: s.cfg.DevUser}
	if err := s.sessions.Issue(w, id); err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}
	rd := r.URL.Query().Get("rd")
	if rd == "" || !strings.HasPrefix(rd, "/") || strings.HasPrefix(rd, "//") {
		rd = "/"
	}
	http.Redirect(w, r, rd, http.StatusFound)
}

// --- middleware ---

type ctxKey int

const identityKey ctxKey = iota

// requireSession loads the identity from the cookie or 401s (API) / redirects
// to login (browser navigation).
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.sessions.Read(r)
		if err != nil {
			if isAPIRequest(r) {
				writeError(w, http.StatusUnauthorized, "not authenticated")
			} else {
				http.Redirect(w, r, "/auth/login?rd="+r.URL.Path, http.StatusFound)
			}
			return
		}
		ctx := context.WithValue(r.Context(), identityKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireCSRF enforces the custom-header defence for mutating requests.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Stepshell") != "1" {
			writeError(w, http.StatusForbidden, "missing X-Stepshell header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func identityFrom(ctx context.Context) session.Identity {
	id, _ := ctx.Value(identityKey).(session.Identity)
	return id
}

func isAPIRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws/")
}
