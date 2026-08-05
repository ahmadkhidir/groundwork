package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// RegisterAuthRoutes wires FEAT-001's endpoints onto r.
//
// This RegisterXRoutes(r chi.Router, deps...) shape is the pattern every later wave's route file
// follows — established here as Wave 1's shared-wiring contribution.
func RegisterAuthRoutes(r chi.Router, service *auth.Service, logger *slog.Logger) {
	r.Post("/auth/signup", handleSignup(service, logger))
	r.Post("/auth/login", handleLogin(service, logger))
	r.Post("/auth/password-reset/request", handlePasswordResetRequest(service, logger))
	r.Post("/auth/password-reset/confirm", handlePasswordResetConfirm(service, logger))
}

// credentialsRequest is the shape POSTed by both signup and login — email plus password.
type credentialsRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// publicUser is the wire shape for a user in an API response.
//
// Deliberately its own type rather than domain.User: domain.User already excludes the password
// hash (schema.md's design), but keeping a separate response type means a future field added to
// domain.User for internal use can never leak into a client response by accident — there is no
// field for it to ride along on.
type publicUser struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Tier        string `json:"tier"`
	CreatedAt   string `json:"created_at"`
}

type authUserResponse struct {
	User publicUser `json:"user"`
}

func toPublicUser(u domain.User) publicUser {
	return publicUser{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Tier:        string(u.Tier),
		CreatedAt:   u.CreatedAt.Format(time.RFC3339),
	}
}

// handleSignup creates a new account. Establishing a session is the Next.js tier's job (it calls
// this endpoint's sibling, /v1/auth/login, via the Credentials provider in web/src/auth.ts) — this
// handler only creates the row.
func handleSignup(service *auth.Service, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req credentialsRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		user, err := service.SignUp(r.Context(), req.Email, req.Password)
		if err != nil {
			writeSignupError(w, logger, err)
			return
		}

		respondJSON(w, http.StatusCreated, authUserResponse{User: toPublicUser(user)})
	}
}

// handleLogin verifies credentials for the Next.js Credentials provider (web/src/auth.ts). It is the
// ONLY place the Next.js tier's session logic touches user data — the Go service owns the users
// table exclusively, per architecture.md's boundary rule.
func handleLogin(service *auth.Service, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req credentialsRequest
		if !decodeJSON(w, r, &req) {
			return
		}

		user, err := service.Authenticate(r.Context(), req.Email, req.Password)
		if err != nil {
			// One generic response for every failure. Wrong password and "no such account" MUST be
			// indistinguishable to the client — see auth.Service.Authenticate.
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Incorrect email or password.",
				"code":  "invalid_credentials",
			})
			return
		}

		respondJSON(w, http.StatusOK, authUserResponse{User: toPublicUser(user)})
	}
}

type passwordResetRequestBody struct {
	Email string `json:"email"`
}

// handlePasswordResetRequest always responds 202 with the same generic message, whether or not the
// email belongs to an account — see auth.Service.RequestPasswordReset.
func handlePasswordResetRequest(service *auth.Service, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req passwordResetRequestBody
		if !decodeJSON(w, r, &req) {
			return
		}

		if err := service.RequestPasswordReset(r.Context(), req.Email); err != nil {
			logger.Error("password reset request failed", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusAccepted, map[string]string{
			"message": "If an account exists for that email, reset instructions have been sent.",
		})
	}
}

type passwordResetConfirmBody struct {
	Token       string `json:"token"`
	NewPassword string `json:"new_password"`
}

func handlePasswordResetConfirm(service *auth.Service, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req passwordResetConfirmBody
		if !decodeJSON(w, r, &req) {
			return
		}

		err := service.ConfirmPasswordReset(r.Context(), req.Token, req.NewPassword)
		switch {
		case err == nil:
			respondJSON(w, http.StatusOK, map[string]string{"message": "Password updated."})
		case errors.Is(err, auth.ErrInvalidResetToken):
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "This reset link is invalid or has expired.",
				"code":  "invalid_reset_token",
			})
		case errors.Is(err, auth.ErrPasswordRequired):
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": err.Error(),
				"code":  "validation_error",
			})
		default:
			logger.Error("password reset confirm failed", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
		}
	}
}

// writeSignupError maps a Service.SignUp error to an HTTP response.
//
// Unlike login, revealing "an account with that email already exists" at signup is not treated as
// an enumeration leak here: screens/02-sign-up-login.md's Auth Error state explicitly names
// "account exists (sign up)" as a distinct, user-facing condition from wrong credentials at login,
// and a signup attempt already asserts the email belongs to the person submitting it.
func writeSignupError(w http.ResponseWriter, logger *slog.Logger, err error) {
	switch {
	case errors.Is(err, auth.ErrEmailTaken):
		respondJSON(w, http.StatusConflict, map[string]string{
			"error": "An account with that email already exists.",
			"code":  "email_taken",
		})
	case errors.Is(err, auth.ErrInvalidEmail):
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
			"code":  "invalid_email",
		})
	case errors.Is(err, auth.ErrPasswordRequired):
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
			"code":  "validation_error",
		})
	default:
		logger.Error("signup failed", slog.String("error", err.Error()))
		respondJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "Something went wrong. Try again.",
			"code":  "internal_error",
		})
	}
}

// decodeJSON decodes r's body into dest, writing a 400 response and returning false on failure so
// handlers can write `if !decodeJSON(w, r, &req) { return }`.
func decodeJSON(w http.ResponseWriter, r *http.Request, dest any) bool {
	if err := json.NewDecoder(r.Body).Decode(dest); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{
			"error": "Malformed request body.",
			"code":  "bad_request",
		})
		return false
	}
	return true
}
