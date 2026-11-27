// Package config loads and validates application settings from environment variables.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDatabaseDSN       = "postgres://postgres:postgres@localhost:5432/meeting_notes?sslmode=disable"
	defaultStorageDir        = "./data/uploads"
	defaultWorkers           = 2
	defaultSpeechTimeout     = 30 * time.Second
	defaultLLMTimeout        = 30 * time.Second
	defaultPollInterval      = time.Second
	defaultShutdownTimeout   = 15 * time.Second
	defaultDatabaseTimeout   = 5 * time.Second
	defaultMaxUploadSize     = int64(100 << 20)
	defaultProcessingTimeout = 70 * time.Second
	defaultRecoveryInterval  = time.Minute
)

// Config contains all runtime settings.
type Config struct {
	DatabaseDSN            string
	StorageDir             string
	SpeechProvider         string
	LLMProvider            string
	Workers                int
	SpeechTimeout          time.Duration
	LLMTimeout             time.Duration
	WorkerPollInterval     time.Duration
	ShutdownTimeout        time.Duration
	DatabaseTimeout        time.Duration
	ProcessingTimeout      time.Duration
	WorkerRecoveryInterval time.Duration
	MaxUploadSize          int64
	LogLevel               slog.Level
}

// Load reads environment variables and returns validated settings.
func Load() (Config, error) {
	cfg := Config{
		DatabaseDSN:            envString("DATABASE_DSN", defaultDatabaseDSN),
		StorageDir:             envString("STORAGE_DIR", defaultStorageDir),
		SpeechProvider:         envString("SPEECH_PROVIDER", "mock"),
		LLMProvider:            envString("LLM_PROVIDER", "mock"),
		Workers:                defaultWorkers,
		SpeechTimeout:          defaultSpeechTimeout,
		LLMTimeout:             defaultLLMTimeout,
		WorkerPollInterval:     defaultPollInterval,
		ShutdownTimeout:        defaultShutdownTimeout,
		DatabaseTimeout:        defaultDatabaseTimeout,
		ProcessingTimeout:      defaultProcessingTimeout,
		WorkerRecoveryInterval: defaultRecoveryInterval,
		MaxUploadSize:          defaultMaxUploadSize,
		LogLevel:               slog.LevelInfo,
	}

	var err error
	if cfg.Workers, err = envInt("WORKERS", cfg.Workers); err != nil {
		return Config{}, err
	}
	if cfg.SpeechTimeout, err = envDuration("SPEECH_TIMEOUT", cfg.SpeechTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LLMTimeout, err = envDuration("LLM_TIMEOUT", cfg.LLMTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WorkerPollInterval, err = envDuration("WORKER_POLL_INTERVAL", cfg.WorkerPollInterval); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = envDuration("SHUTDOWN_TIMEOUT", cfg.ShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseTimeout, err = envDuration("DATABASE_TIMEOUT", cfg.DatabaseTimeout); err != nil {
		return Config{}, err
	}
	if cfg.ProcessingTimeout, err = envDuration("PROCESSING_TIMEOUT", cfg.ProcessingTimeout); err != nil {
		return Config{}, err
	}
	if cfg.WorkerRecoveryInterval, err = envDuration("WORKER_RECOVERY_INTERVAL", cfg.WorkerRecoveryInterval); err != nil {
		return Config{}, err
	}
	if cfg.MaxUploadSize, err = envInt64("MAX_UPLOAD_SIZE", cfg.MaxUploadSize); err != nil {
		return Config{}, err
	}
	if err := cfg.parseLogLevel(envString("LOG_LEVEL", "info")); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.DatabaseDSN) == "" {
		return fmt.Errorf("DATABASE_DSN must not be empty")
	}
	if strings.TrimSpace(c.StorageDir) == "" {
		return fmt.Errorf("STORAGE_DIR must not be empty")
	}
	if c.SpeechProvider != "mock" {
		return fmt.Errorf("unsupported SPEECH_PROVIDER %q", c.SpeechProvider)
	}
	if c.LLMProvider != "mock" {
		return fmt.Errorf("unsupported LLM_PROVIDER %q", c.LLMProvider)
	}
	if c.Workers < 1 {
		return fmt.Errorf("WORKERS must be greater than zero")
	}
	if c.SpeechTimeout <= 0 || c.LLMTimeout <= 0 || c.WorkerPollInterval <= 0 ||
		c.ShutdownTimeout <= 0 || c.DatabaseTimeout <= 0 || c.ProcessingTimeout <= 0 || c.WorkerRecoveryInterval <= 0 {
		return fmt.Errorf("timeouts and polling interval must be greater than zero")
	}
	if c.MaxUploadSize <= 0 {
		return fmt.Errorf("MAX_UPLOAD_SIZE must be greater than zero")
	}
	return nil
}

func (c *Config) parseLogLevel(value string) error {
	if err := c.LogLevel.UnmarshalText([]byte(value)); err != nil {
		return fmt.Errorf("parse LOG_LEVEL: %w", err)
	}
	return nil
}

func envString(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) (int, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func envInt64(name string, fallback int64) (int64, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", name, err)
	}
	return parsed, nil
}
