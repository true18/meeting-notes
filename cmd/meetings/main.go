package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/true18/meeting-notes/internal/app"
	"github.com/true18/meeting-notes/internal/cli"
	"github.com/true18/meeting-notes/internal/config"
	llmmock "github.com/true18/meeting-notes/internal/llm/mock"
	"github.com/true18/meeting-notes/internal/repository/postgres"
	speechmock "github.com/true18/meeting-notes/internal/speech/mock"
	"github.com/true18/meeting-notes/internal/storage"
	"github.com/true18/meeting-notes/internal/worker"
	"github.com/true18/meeting-notes/migrations"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configuration:", err)
		return 1
	}
	command, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	store, err := postgres.Open(signalCtx, cfg.DatabaseDSN, cfg.DatabaseTimeout)
	if err != nil {
		logger.Error("database connection failed", "error", err)
		return 1
	}
	defer store.Close()

	files := storage.New(cfg.StorageDir, cfg.MaxUploadSize)
	speechClient := speechmock.New()
	llmClient := llmmock.New()
	service := app.New(store, files, llmClient, logger)
	pool := worker.New(store, speechClient, llmClient, worker.Config{
		Workers: cfg.Workers, PollInterval: cfg.WorkerPollInterval,
		SpeechTimeout: cfg.SpeechTimeout, LLMTimeout: cfg.LLMTimeout,
		ProcessingTimeout: cfg.ProcessingTimeout, CleanupTimeout: cfg.ShutdownTimeout,
	}, logger)

	handler := cli.Handler{
		Service: service,
		Migrate: func(ctx context.Context) error { return migrations.Apply(ctx, store.Pool()) },
		RunWorker: func(ctx context.Context) error {
			return runWorker(ctx, pool, cfg.ShutdownTimeout)
		},
	}
	if command.Name == "worker" {
		return handler.Execute(signalCtx, command, os.Stdout, os.Stderr)
	}
	operationTimeout := cfg.DatabaseTimeout
	if cfg.LLMTimeout > operationTimeout {
		operationTimeout = cfg.LLMTimeout
	}
	operationCtx, cancel := context.WithTimeout(signalCtx, operationTimeout+time.Second)
	defer cancel()
	return handler.Execute(operationCtx, command, os.Stdout, os.Stderr)
}

type workerRunner interface {
	Run(context.Context) error
}

func runWorker(ctx context.Context, pool workerRunner, shutdownTimeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- pool.Run(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("worker shutdown timeout: %w", context.DeadlineExceeded)
	}
}
