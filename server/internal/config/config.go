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
	"sort"
	"strconv"
	"strings"
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

	// AuthSecret is the Auth.js signing secret, shared with the web tier.
	//
	// The browser calls this service directly and authenticates with the Auth.js session cookie, so
	// this service must decode that cookie itself (see internal/authsession). This value MUST be
	// byte-identical to the web tier's AUTH_SECRET or every authenticated request fails.
	AuthSecret string

	// Storage is the S3-compatible object storage configuration: MinIO locally, Cloudflare R2 in
	// production.
	Storage StorageConfig
}

// StorageConfig describes the S3-compatible object store holding uploaded and normalized datasets.
type StorageConfig struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	// Region defaults to "auto", which is what Cloudflare R2 expects.
	Region string
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

	cfg.AuthSecret = os.Getenv("AUTH_SECRET")

	cfg.Storage = StorageConfig{
		Endpoint:        os.Getenv("S3_ENDPOINT"),
		Bucket:          os.Getenv("S3_BUCKET"),
		AccessKeyID:     os.Getenv("S3_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("S3_SECRET_ACCESS_KEY"),
		Region:          envOrDefault("S3_REGION", "auto"),
	}

	// Everything required is validated in one place and reported together, so a fresh developer
	// fixes their whole .env in one pass instead of rediscovering one missing key per restart.
	var missing []string
	for name, value := range map[string]string{
		"DATABASE_URL":         cfg.DatabaseURL,
		"AUTH_SECRET":          cfg.AuthSecret,
		"S3_ENDPOINT":          cfg.Storage.Endpoint,
		"S3_BUCKET":            cfg.Storage.Bucket,
		"S3_ACCESS_KEY_ID":     cfg.Storage.AccessKeyID,
		"S3_SECRET_ACCESS_KEY": cfg.Storage.SecretAccessKey,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		// Sorted so the message is deterministic; Go map iteration order is not.
		sort.Strings(missing)
		return Config{}, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
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
