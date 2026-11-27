// Package worker processes durable meeting tasks with bounded concurrency.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/llm"
	"github.com/true18/meeting-notes/internal/speech"
)

// Repository is the task persistence boundary used by workers.
type Repository interface {
	ClaimTask(context.Context, time.Duration) (domain.ClaimedTask, bool, error)
	SaveTranscript(context.Context, int64, int64, int64, string) error
	CompleteTask(context.Context, int64, int64, int64, string) error
	FailTask(context.Context, int64, int64, string) error
	RequeueTask(context.Context, int64, int64, string) error
	RecoverStaleTasks(context.Context) (int64, error)
}

// Config controls worker concurrency and timeouts.
type Config struct {
	Workers           int
	PollInterval      time.Duration
	SpeechTimeout     time.Duration
	LLMTimeout        time.Duration
	ProcessingTimeout time.Duration
	CleanupTimeout    time.Duration
	RecoveryInterval  time.Duration
}

// Pool runs a fixed number of task-processing goroutines.
type Pool struct {
	repo   Repository
	speech speech.Client
	llm    llm.Client
	config Config
	logger *slog.Logger
}

// New creates a bounded worker pool.
func New(repo Repository, speechClient speech.Client, llmClient llm.Client, config Config, logger *slog.Logger) *Pool {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if config.CleanupTimeout <= 0 {
		config.CleanupTimeout = 3 * time.Second
	}
	return &Pool{repo: repo, speech: speechClient, llm: llmClient, config: config, logger: logger}
}

// Run processes tasks until ctx is canceled and all workers have stopped.
func (p *Pool) Run(ctx context.Context) error {
	if p.config.Workers < 1 {
		return errors.New("worker count must be greater than zero")
	}
	recovered, err := p.repo.RecoverStaleTasks(ctx)
	if err != nil {
		return fmt.Errorf("recover stale tasks: %w", err)
	}
	if recovered > 0 {
		p.logger.WarnContext(ctx, "recovered stale tasks", "count", recovered)
	}

	var wait sync.WaitGroup
	wait.Add(p.config.Workers + 1)
	go func() {
		defer wait.Done()
		p.recoverPeriodically(ctx)
	}()
	for id := 1; id <= p.config.Workers; id++ {
		go func(workerID int) {
			defer wait.Done()
			p.runWorker(ctx, workerID)
		}(id)
	}
	wait.Wait()
	p.logger.Info("worker pool stopped")
	return nil
}

func (p *Pool) recoverPeriodically(ctx context.Context) {
	interval := p.config.RecoveryInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recovered, err := p.repo.RecoverStaleTasks(ctx)
			if err != nil {
				p.logger.ErrorContext(ctx, "recover stale tasks", "error", err)
				continue
			}
			if recovered > 0 {
				p.logger.WarnContext(ctx, "recovered stale tasks", "count", recovered)
			}
		}
	}
}

func (p *Pool) runWorker(ctx context.Context, workerID int) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		lease := p.config.ProcessingTimeout + p.config.CleanupTimeout + 5*time.Second
		task, found, err := p.repo.ClaimTask(ctx, lease)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			p.logger.ErrorContext(ctx, "claim task", "worker_id", workerID, "error", err)
			timer.Reset(p.config.PollInterval)
			continue
		}
		if !found {
			timer.Reset(p.config.PollInterval)
			continue
		}
		p.process(ctx, workerID, task)
		timer.Reset(0)
	}
}

func (p *Pool) process(parent context.Context, workerID int, task domain.ClaimedTask) {
	ctx, cancel := context.WithTimeout(parent, p.config.ProcessingTimeout)
	defer cancel()
	p.logger.InfoContext(ctx, "processing meeting", "worker_id", workerID, "meeting_id", task.MeetingID, "task_id", task.TaskID)

	speechCtx, cancelSpeech := context.WithTimeout(ctx, p.config.SpeechTimeout)
	p.logger.DebugContext(speechCtx, "calling speech client", "meeting_id", task.MeetingID, "task_id", task.TaskID)
	transcript, err := p.speech.Transcribe(speechCtx, speech.Request{Path: task.FilePath, Name: task.MeetingName, Format: task.FileFormat})
	cancelSpeech()
	if err != nil {
		p.finishError(parent, task, fmt.Errorf("transcribe meeting: %w", err))
		return
	}
	if err := p.repo.SaveTranscript(ctx, task.TaskID, task.Attempt, task.MeetingID, transcript); err != nil {
		p.finishError(parent, task, fmt.Errorf("save transcript: %w", err))
		return
	}
	p.logger.InfoContext(ctx, "meeting transcribed", "meeting_id", task.MeetingID, "task_id", task.TaskID)

	llmCtx, cancelLLM := context.WithTimeout(ctx, p.config.LLMTimeout)
	p.logger.DebugContext(llmCtx, "calling LLM client", "meeting_id", task.MeetingID, "task_id", task.TaskID)
	summary, err := p.llm.Summarize(llmCtx, transcript)
	cancelLLM()
	if err != nil {
		p.finishError(parent, task, fmt.Errorf("summarize meeting: %w", err))
		return
	}
	if err := p.repo.CompleteTask(ctx, task.TaskID, task.Attempt, task.MeetingID, summary); err != nil {
		p.finishError(parent, task, fmt.Errorf("save summary: %w", err))
		return
	}
	p.logger.InfoContext(ctx, "meeting completed", "meeting_id", task.MeetingID, "task_id", task.TaskID)
}

func (p *Pool) finishError(parent context.Context, task domain.ClaimedTask, processingErr error) {
	cleanupCtx, cancel := context.WithTimeout(context.Background(), p.config.CleanupTimeout)
	defer cancel()
	if parent.Err() != nil {
		if err := p.repo.RequeueTask(cleanupCtx, task.TaskID, task.Attempt, processingErr.Error()); err != nil {
			p.logger.Error("requeue interrupted task", "task_id", task.TaskID, "error", err)
		}
		p.logger.Warn("task interrupted and requeued", "task_id", task.TaskID, "error", processingErr)
		return
	}
	if err := p.repo.FailTask(cleanupCtx, task.TaskID, task.Attempt, processingErr.Error()); err != nil {
		p.logger.Error("persist task failure", "task_id", task.TaskID, "processing_error", processingErr, "error", err)
		return
	}
	p.logger.Error("meeting processing failed", "meeting_id", task.MeetingID, "task_id", task.TaskID, "error", processingErr)
}
