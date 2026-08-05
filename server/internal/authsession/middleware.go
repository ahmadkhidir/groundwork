package authsession

import (
	"context"
	"encoding/json"
	"net/http"
)

type contextKey string

const claimsContextKey contextKey = "authsession.claims"

// RequireAuth returns middleware that rejects any request without a valid Auth.js session cookie,
// and otherwise attaches the decoded Claims to the request context for downstream handlers.
//
// This is the actual authorization boundary — see this package's doc comment. secret must be the
// same AUTH_SECRET value the Next.js tier signs cookies with.
func RequireAuth(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := FromRequest(r, secret)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"error": "You must be signed in to do that.",
					"code":  "unauthenticated",
				})
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// FromContext retrieves the Claims attached by RequireAuth. ok is false if no middleware ran, which
// a handler should treat as a bug (mounting the route outside RequireAuth) rather than recover from.
func FromContext(ctx context.Context) (Claims, bool) {
	claims, ok := ctx.Value(claimsContextKey).(Claims)
	return claims, ok
}
