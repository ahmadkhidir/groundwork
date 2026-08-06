package config

import (
	"testing"
	"time"
)

func TestMaxUploadBytesDefaultIs500MB(t *testing.T) {
	// The confirmed product decision (2026-08-05). features.md FEAT-004 states only "a stated size
	// ceiling"; this constant is that figure, and the web client mirrors it.
	const want = 524_288_000 // 500 * 1024 * 1024

	if MaxUploadBytesDefault != want {
		t.Errorf("MaxUploadBytesDefault = %d, want %d (500 MB)", MaxUploadBytesDefault, want)
	}
}

func TestMaxExecuteRowsIsTenThousand(t *testing.T) {
	// The confirmed product decision (2026-08-06). features.md FEAT-014 AC-3 references the product's
	// "stated row-count ceiling" without a figure; this constant is that figure, applied both to the
	// execute step (a query past it fails the run) and to the narrate/chart_spec facts payload.
	const want = 10000

	if MaxExecuteRows != want {
		t.Errorf("MaxExecuteRows = %d, want %d", MaxExecuteRows, want)
	}
}

// requiredEnv is the minimum set that lets Load succeed. Tests start from this and override or
// remove individual keys, so adding a new required variable breaks one obvious place rather than
// every case at once.
func requiredEnv() map[string]string {
	return map[string]string{
		"DATABASE_URL":         "postgres://localhost/groundwork",
		"AUTH_SECRET":          "test-secret",
		"S3_ENDPOINT":          "http://localhost:9000",
		"S3_BUCKET":            "groundwork-datasets",
		"S3_ACCESS_KEY_ID":     "test-key",
		"S3_SECRET_ACCESS_KEY": "test-secret-key",
	}
}

// withEnv returns the required set with the given overrides applied. A "" value removes the key,
// which is how a test asserts that a variable really is required.
func withEnv(overrides map[string]string) map[string]string {
	env := requiredEnv()
	for key, value := range overrides {
		if value == "" {
			delete(env, key)
			continue
		}
		env[key] = value
	}
	return env
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "applies documented defaults",
			env:  requiredEnv(),
			check: func(t *testing.T, cfg Config) {
				if cfg.MaxUploadBytes != MaxUploadBytesDefault {
					t.Errorf("MaxUploadBytes = %d, want %d", cfg.MaxUploadBytes, MaxUploadBytesDefault)
				}
				if cfg.EngineIdleTimeout != 10*time.Minute {
					t.Errorf("EngineIdleTimeout = %v, want 10m", cfg.EngineIdleTimeout)
				}
				if cfg.Port != "8080" {
					t.Errorf("Port = %q, want 8080", cfg.Port)
				}
				if cfg.IsProduction() {
					t.Error("IsProduction() = true, want false for the development default")
				}
				if cfg.Storage.Region != "auto" {
					t.Errorf("Storage.Region = %q, want auto (Cloudflare R2's expected value)", cfg.Storage.Region)
				}
			},
		},
		{
			// Refusing to start beats starting against the wrong database, or none.
			name:    "rejects a missing database URL",
			env:     withEnv(map[string]string{"DATABASE_URL": ""}),
			wantErr: true,
		},
		{
			name: "allows overriding the upload ceiling per environment",
			env:  withEnv(map[string]string{"MAX_UPLOAD_BYTES": "1048576"}),
			check: func(t *testing.T, cfg Config) {
				if cfg.MaxUploadBytes != 1048576 {
					t.Errorf("MaxUploadBytes = %d, want 1048576", cfg.MaxUploadBytes)
				}
			},
		},
		{
			name:    "rejects a non-numeric upload ceiling",
			env:     withEnv(map[string]string{"MAX_UPLOAD_BYTES": "500MB"}),
			wantErr: true,
		},
		{
			// A zero or negative ceiling would reject every upload — fail loudly at boot instead.
			name:    "rejects a non-positive upload ceiling",
			env:     withEnv(map[string]string{"MAX_UPLOAD_BYTES": "0"}),
			wantErr: true,
		},
		{
			name:    "rejects an unparseable idle timeout",
			env:     withEnv(map[string]string{"ENGINE_IDLE_TIMEOUT": "ten minutes"}),
			wantErr: true,
		},
		{
			name: "detects the production environment",
			env:  withEnv(map[string]string{"ENVIRONMENT": "production"}),
			check: func(t *testing.T, cfg Config) {
				if !cfg.IsProduction() {
					t.Error("IsProduction() = false, want true")
				}
			},
		},
		{
			// The web tier and this service must share AUTH_SECRET byte-for-byte, or every
			// authenticated request fails at cookie decode. Starting without it is never useful.
			name:    "rejects a missing AUTH_SECRET",
			env:     withEnv(map[string]string{"AUTH_SECRET": ""}),
			wantErr: true,
		},
		{
			name:    "rejects missing object storage settings",
			env:     withEnv(map[string]string{"S3_BUCKET": ""}),
			wantErr: true,
		},
		{
			name: "reads object storage settings into Storage",
			env:  requiredEnv(),
			check: func(t *testing.T, cfg Config) {
				if cfg.Storage.Bucket != "groundwork-datasets" {
					t.Errorf("Storage.Bucket = %q", cfg.Storage.Bucket)
				}
				if cfg.AuthSecret != "test-secret" {
					t.Errorf("AuthSecret = %q", cfg.AuthSecret)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv restores the previous value automatically and forbids parallel execution,
			// which is exactly right for process-global state like the environment.
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			// Ensure inherited values from the developer's shell cannot influence the result.
			for _, key := range []string{
				"MAX_UPLOAD_BYTES", "ENGINE_IDLE_TIMEOUT", "ENVIRONMENT", "PORT", "DATABASE_URL",
				"AUTH_SECRET", "S3_ENDPOINT", "S3_BUCKET", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY", "S3_REGION",
			} {
				if _, set := tt.env[key]; !set {
					t.Setenv(key, "")
				}
			}

			cfg, err := Load()

			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v, want nil", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}
