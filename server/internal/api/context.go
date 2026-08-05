package api

import (
	"net/http"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
)

// requireClaims retrieves the authsession.Claims attached by authsession.RequireAuth.
//
// A missing value here means a route was mounted outside RequireAuth's middleware chain — a wiring
// bug, not a client error — so this responds 401 defensively rather than panicking, but every route
// registered through api.go's protected group is expected to always have claims present.
func requireClaims(w http.ResponseWriter, r *http.Request) (authsession.Claims, bool) {
	claims, ok := authsession.FromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "You must be signed in to do that.",
			"code":  "unauthenticated",
		})
		return authsession.Claims{}, false
	}
	return claims, true
}
