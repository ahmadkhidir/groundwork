// Package config holds every tunable threshold and environment-derived setting for the API service.
//
// Thresholds live here and nowhere else. A limit scattered as a literal across handlers cannot be
// audited, cannot be tuned per environment, and drifts out of step with the client that enforces
// the same rule. See IMPLEMENTATION.md §5 rule 12.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// MaxUploadBytesDefault is the ceiling on a single uploaded file: 500 MB.
//
// Confirmed by the product owner on 2026-08-05. features.md FEAT-004 specifies only "a stated size
// ceiling" without a figure, so this constant is the figure.
//
// It applies PER FILE and across ALL tiers: monetization.md gates usage allotments (engine-seconds,
// storage, model tokens), never per-file size. The web client validates against the same value
// before upload starts, but that is a UX affordance — this check is the control.
const MaxUploadBytesDefault int64 = 500 * 1024 * 1024

// EngineIdleTimeoutDefault is how long a session engine may sit idle before teardown.
//
// Per architecture.md, wake latency is dominated by compute provisioning rather than dataset size,
// so tearing down an idle engine is cheap to reverse and saves metered engine-seconds.
const EngineIdleTimeoutDefault = 10 * time.Minute

// Config is the fully resolved service configuration.
type Config struct {
	// Port the HTTP server listens on.
	Port string

	// DatabaseURL is the Postgres connection string for application state.
	DatabaseURL string

	// AllowedOrigin is the web client's origin, used for CORS. Never "*": credentials are sent on
	// every API request, and a wildcard origin with credentials is both invalid and unsafe.
	AllowedOrigin string

	// MaxUploadBytes is the per-file upload ceiling in bytes.
	MaxUploadBytes int64

	// EngineIdleTimeout is how long an engine may idle before teardown.
	EngineIdleTimeout time.Duration

	// Environment is "development", "staging", or "production". Controls error verbosity.
	Environment string
}

// IsProduction reports whether the service is running in the production environment.
func (c Config) IsProduction() bool {
	return c.Environment == "production"
}

// Load reads configuration from the environment and validates it.
//
// It returns an error rather than falling back to a default for anything security-relevant or
// environment-specific: a service that silently starts against the wrong database is worse than one
// that refuses to start.
func Load() (Config, error) {
	cfg := Config{
		Port:              envOrDefault("PORT", "8080"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AllowedOrigin:     envOrDefault("ALLOWED_ORIGIN", "http://localhost:3000"),
		MaxUploadBytes:    MaxUploadBytesDefault,
		EngineIdleTimeout: EngineIdleTimeoutDefault,
		Environment:       envOrDefault("ENVIRONMENT", "development"),
	}

	if raw := os.Getenv("MAX_UPLOAD_BYTES"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return Config{}, fmt.Errorf("parsing MAX_UPLOAD_BYTES: %w", err)
		}
		if parsed <= 0 {
			return Config{}, errors.New("MAX_UPLOAD_BYTES must be positive")
		}
		cfg.MaxUploadBytes = parsed
	}

	if raw := os.Getenv("ENGINE_IDLE_TIMEOUT"); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return Config{}, fmt.Errorf("parsing ENGINE_IDLE_TIMEOUT: %w", err)
		}
		cfg.EngineIdleTimeout = parsed
	}

	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}

	return cfg, nil
}

// envOrDefault returns the environment variable's value, or fallback when it is unset or empty.
func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
