package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// authService is nil and Dependencies is the zero value: these tests exercise routing and CORS
	// only, none of which touch FEAT-001/002/003/042. Auth-specific behavior is covered by
	// internal/api/auth_test.go; workspace/upload/audit-log behavior by their own _test.go files.
	return NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, Dependencies{})
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
//
// FEAT-002/003/042 replaced the /v1/workspaces/ placeholders this test originally exercised (Wave 2)
// — per IMPLEMENTATION.md, "a feature slice deletes the... 501 handler; it does not build around
// it." handleNotImplemented itself remains live infrastructure for whatever a later wave still needs
// it for, so it is exercised directly here rather than through a route this wave has since built.
func TestHandleNotImplementedReports501WithOwningFeature(t *testing.T) {
	handler := (&Server{}).handleNotImplemented("FEAT-999", "Some Future Phase")

	req := httptest.NewRequest(http.MethodGet, "/v1/whatever", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotImplemented)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["feature"] != "FEAT-999" {
		t.Errorf("feature = %q, want FEAT-999", body["feature"])
	}
	if body["code"] != "not_implemented" {
		t.Errorf("code = %q, want not_implemented", body["code"])
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

	// x-request-id must be readable by the cross-origin web tier (Vercel web → Fly API) so a user
	// can report an error by its correlation id; without Expose-Headers fetch hides it from JS.
	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(strings.ToLower(exposed), "x-request-id") {
		t.Errorf("Expose-Headers = %q, want it to include x-request-id", exposed)
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
