package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
)

// newTestServer builds a Server with logging discarded, so test output stays readable.
func newTestServer() *Server {
	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	// authService is nil: these tests exercise routing, CORS, and the not-implemented shell, none of
	// which touch FEAT-001. Auth-specific behavior is covered by internal/api/auth_test.go.
	return NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestHealthEndpoint(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

// An unbuilt endpoint must say so, and say which feature will build it. A 404 would be
// indistinguishable from a typo in the path.
func TestUnimplementedEndpointsReport501WithOwningFeature(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/workspaces/"},
		{http.MethodPost, "/v1/workspaces/"},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			newTestServer().ServeHTTP(rec, req)

			if rec.Code != http.StatusNotImplemented {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
			}

			var body map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body["feature"] == "" {
				t.Error("response omits the owning feature id")
			}
			if body["code"] != "not_implemented" {
				t.Errorf("code = %q, want not_implemented", body["code"])
			}
		})
	}
}

func TestCORSAllowsTheConfiguredOriginWithCredentials(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	newTestServer().ServeHTTP(rec, req)

	origin := rec.Header().Get("Access-Control-Allow-Origin")
	if origin != "http://localhost:3000" {
		t.Errorf("Allow-Origin = %q, want the configured web origin", origin)
	}

	// A wildcard origin combined with credentials is rejected by browsers and unsafe in principle.
	// This asserts the pairing can never regress to "*".
	if origin == "*" {
		t.Error("Allow-Origin must never be a wildcard while credentials are allowed")
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("Allow-Credentials must be true: session cookies ride on every API request")
	}
}

func TestPreflightShortCircuits(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/v1/workspaces/", nil)
	rec := httptest.NewRecorder()

	newTestServer().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want %d for a preflight request", rec.Code, http.StatusNoContent)
	}
}
