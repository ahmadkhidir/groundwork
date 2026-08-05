// Package api holds the HTTP layer: routing, middleware, and response shaping.
//
// This service is the authorization boundary. The Next.js tier's proxy check is an optimistic
// redirect for the user's benefit; every handler here re-verifies identity and workspace scope
// regardless of what the client asserts.
package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
)

// Server wires configuration and dependencies into an http.Handler.
type Server struct {
	cfg    config.Config
	logger *slog.Logger
	router chi.Router

	authService *auth.Service
}

// NewServer constructs a Server with all routes registered.
//
// authService may be nil only in tests that don't exercise auth-backed routes; production callers
// (cmd/api/main.go) always supply one.
func NewServer(cfg config.Config, logger *slog.Logger, authService *auth.Service) *Server {
	s := &Server{
		cfg:         cfg,
		logger:      logger,
		router:      chi.NewRouter(),
		authService: authService,
	}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// routes registers middleware and every endpoint.
//
// Middleware order is significant: RequestID must come first so every later log line and error
// response can carry the correlation id, and Recoverer must wrap the handlers so a panic becomes a
// 500 rather than killing the process.
func (s *Server) routes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(s.requestLogger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.Timeout(60 * time.Second))
	s.router.Use(s.cors)

	// Liveness and readiness. Deliberately unauthenticated: a health check that requires a session
	// cannot be used by a load balancer.
	s.router.Get("/healthz", s.handleHealth)

	s.router.Route("/v1", func(r chi.Router) {
		// FEAT-001: signup, login (credential verification for the Next.js Credentials provider —
		// the web layer never reads the users table itself, per architecture.md's boundary rule),
		// and password reset.
		if s.authService != nil {
			RegisterAuthRoutes(r, s.authService, s.logger)
		}

		r.Route("/workspaces", func(r chi.Router) {
			r.Get("/", s.handleNotImplemented("FEAT-002", "Phase 1"))
			r.Post("/", s.handleNotImplemented("FEAT-002", "Phase 1"))
		})
	})
}

// handleHealth reports service liveness.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, map[string]string{
		"status":      "ok",
		"environment": s.cfg.Environment,
	})
}

// handleNotImplemented returns a handler that honestly reports an unbuilt endpoint.
//
// Foundation registers the route tree so routing, middleware, and CORS are verifiable end to end
// before feature work lands. A 501 naming the owning feature is far more useful than a 404, which
// is indistinguishable from a typo in the path.
func (s *Server) handleNotImplemented(featureID, phase string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respondJSON(w, http.StatusNotImplemented, map[string]string{
			"error":   "This endpoint is not implemented yet.",
			"code":    "not_implemented",
			"feature": featureID,
			"phase":   phase,
		})
	}
}

// requestLogger logs one structured line per request, including the correlation id.
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		wrapped := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(wrapped, r)

		s.logger.Info("request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", wrapped.Status()),
			slog.Duration("duration", time.Since(started)),
			slog.String("request_id", middleware.GetReqID(r.Context())),
		)
	})
}

// cors permits credentialed requests from the configured web origin.
//
// The origin is echoed from configuration rather than reflected from the request, and is never "*":
// the browser sends session cookies on every API call, and a wildcard origin with credentials is
// both rejected by browsers and unsafe in principle.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", s.cfg.AllowedOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Vary", "Origin")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// respondJSON writes a JSON response with the given status code.
func respondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if payload == nil {
		return
	}

	// The header and status are already committed, so an encoding failure cannot be turned into a
	// 500 here. Log-and-continue is the only honest option; the client sees a truncated body.
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("encoding response", slog.String("error", err.Error()))
	}
}
