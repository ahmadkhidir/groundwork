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

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
		check   func(t *testing.T, cfg Config)
	}{
		{
			name: "applies documented defaults",
			env:  map[string]string{"DATABASE_URL": "postgres://localhost/groundwork"},
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
			},
		},
		{
			// Refusing to start beats starting against the wrong database, or none.
			name:    "rejects a missing database URL",
			env:     map[string]string{},
			wantErr: true,
		},
		{
			name: "allows overriding the upload ceiling per environment",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost/groundwork",
				"MAX_UPLOAD_BYTES": "1048576",
			},
			check: func(t *testing.T, cfg Config) {
				if cfg.MaxUploadBytes != 1048576 {
					t.Errorf("MaxUploadBytes = %d, want 1048576", cfg.MaxUploadBytes)
				}
			},
		},
		{
			name: "rejects a non-numeric upload ceiling",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost/groundwork",
				"MAX_UPLOAD_BYTES": "500MB",
			},
			wantErr: true,
		},
		{
			// A zero or negative ceiling would reject every upload — fail loudly at boot instead.
			name: "rejects a non-positive upload ceiling",
			env: map[string]string{
				"DATABASE_URL":     "postgres://localhost/groundwork",
				"MAX_UPLOAD_BYTES": "0",
			},
			wantErr: true,
		},
		{
			name: "rejects an unparseable idle timeout",
			env: map[string]string{
				"DATABASE_URL":        "postgres://localhost/groundwork",
				"ENGINE_IDLE_TIMEOUT": "ten minutes",
			},
			wantErr: true,
		},
		{
			name: "detects the production environment",
			env: map[string]string{
				"DATABASE_URL": "postgres://localhost/groundwork",
				"ENVIRONMENT":  "production",
			},
			check: func(t *testing.T, cfg Config) {
				if !cfg.IsProduction() {
					t.Error("IsProduction() = false, want true")
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
			for _, key := range []string{"MAX_UPLOAD_BYTES", "ENGINE_IDLE_TIMEOUT", "ENVIRONMENT", "PORT", "DATABASE_URL"} {
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
