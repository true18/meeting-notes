package config

import (
	"log/slog"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workers != 2 || cfg.SpeechProvider != "mock" || cfg.LLMProvider != "mock" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.StorageDir == "" || cfg.DatabaseDSN == "" {
		t.Fatalf("incomplete defaults: %+v", cfg)
	}
}

func TestLoadEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("WORKERS", "4")
	t.Setenv("SPEECH_TIMEOUT", "2s")
	t.Setenv("LLM_TIMEOUT", "3s")
	t.Setenv("MAX_UPLOAD_SIZE", "1234")
	t.Setenv("LOG_LEVEL", "debug")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workers != 4 || cfg.SpeechTimeout != 2*time.Second || cfg.LLMTimeout != 3*time.Second || cfg.MaxUploadSize != 1234 {
		t.Fatalf("environment not applied: %+v", cfg)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("LogLevel = %v", cfg.LogLevel)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "workers syntax", key: "WORKERS", value: "many"},
		{name: "workers range", key: "WORKERS", value: "0"},
		{name: "speech provider", key: "SPEECH_PROVIDER", value: "external"},
		{name: "llm provider", key: "LLM_PROVIDER", value: "external"},
		{name: "duration", key: "SPEECH_TIMEOUT", value: "soon"},
		{name: "log level", key: "LOG_LEVEL", value: "verbose"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(tt.key, tt.value)
			if _, err := Load(); err == nil {
				t.Fatal("Load returned nil error")
			}
		})
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DATABASE_DSN", "STORAGE_DIR", "SPEECH_PROVIDER", "LLM_PROVIDER", "WORKERS",
		"SPEECH_TIMEOUT", "LLM_TIMEOUT", "WORKER_POLL_INTERVAL", "SHUTDOWN_TIMEOUT",
		"DATABASE_TIMEOUT", "PROCESSING_TIMEOUT", "MAX_UPLOAD_SIZE", "LOG_LEVEL",
	} {
		t.Setenv(key, "")
	}
	// Empty values are meaningful, so restore defaults by removing values in subprocess-free tests.
	for _, key := range []string{
		"DATABASE_DSN", "STORAGE_DIR", "SPEECH_PROVIDER", "LLM_PROVIDER", "WORKERS",
		"SPEECH_TIMEOUT", "LLM_TIMEOUT", "WORKER_POLL_INTERVAL", "SHUTDOWN_TIMEOUT",
		"DATABASE_TIMEOUT", "PROCESSING_TIMEOUT", "MAX_UPLOAD_SIZE", "LOG_LEVEL",
	} {
		t.Setenv(key, defaultForTest(key))
	}
}

func defaultForTest(key string) string {
	defaults := map[string]string{
		"DATABASE_DSN": "postgres://postgres:postgres@localhost:5432/meeting_notes?sslmode=disable",
		"STORAGE_DIR":  "./data/uploads", "SPEECH_PROVIDER": "mock", "LLM_PROVIDER": "mock",
		"WORKERS": "2", "SPEECH_TIMEOUT": "30s", "LLM_TIMEOUT": "30s",
		"WORKER_POLL_INTERVAL": "1s", "SHUTDOWN_TIMEOUT": "15s", "DATABASE_TIMEOUT": "5s",
		"PROCESSING_TIMEOUT": "70s", "MAX_UPLOAD_SIZE": "104857600", "LOG_LEVEL": "info",
	}
	return defaults[key]
}
