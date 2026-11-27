package worker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/speech"
)

func TestProcessSuccessAndErrors(t *testing.T) {
	config := testConfig()
	t.Run("success", func(t *testing.T) {
		repo := &fakeRepository{}
		pool := New(repo, staticSpeech{text: "release transcript"}, staticLLM{summary: "summary"}, config, nil)
		pool.process(context.Background(), 1, domain.ClaimedTask{TaskID: 1, MeetingID: 2, FileFormat: "txt"})
		if repo.transcript != "release transcript" || repo.summary != "summary" || repo.failed != "" {
			t.Fatalf("repository state: %+v", repo)
		}
	})
	t.Run("speech failure", func(t *testing.T) {
		repo := &fakeRepository{}
		pool := New(repo, staticSpeech{err: errors.New("speech down")}, staticLLM{}, config, nil)
		pool.process(context.Background(), 1, domain.ClaimedTask{TaskID: 3, MeetingID: 4})
		if repo.failed == "" || repo.transcript != "" {
			t.Fatalf("repository state: %+v", repo)
		}
	})
	t.Run("LLM failure keeps transcript and fails task", func(t *testing.T) {
		repo := &fakeRepository{}
		pool := New(repo, staticSpeech{text: "text"}, staticLLM{err: errors.New("LLM down")}, config, nil)
		pool.process(context.Background(), 1, domain.ClaimedTask{TaskID: 5, MeetingID: 6})
		if repo.transcript != "text" || repo.failed == "" || repo.summary != "" {
			t.Fatalf("repository state: %+v", repo)
		}
	})
}

func TestProcessRequeuesOnShutdown(t *testing.T) {
	repo := &fakeRepository{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool := New(repo, blockingSpeech{}, staticLLM{}, testConfig(), nil)
	pool.process(ctx, 1, domain.ClaimedTask{TaskID: 7, MeetingID: 8})
	if repo.requeued == "" || repo.failed != "" {
		t.Fatalf("repository state: %+v", repo)
	}
}

func TestPeriodicRecoveryContinuesAfterErrorAndStops(t *testing.T) {
	repo := &periodicRecoveryRepository{calls: make(chan int, 8)}
	pool := New(repo, staticSpeech{}, staticLLM{}, testConfig(), nil)
	pool.config.RecoveryInterval = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { pool.recoverPeriodically(ctx); close(done) }()
	for want := 1; want <= 2; want++ {
		select {
		case got := <-repo.calls:
			if got != want {
				t.Fatalf("recovery call = %d, want %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("periodic recovery did not continue")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovery goroutine did not stop")
	}
}

type periodicRecoveryRepository struct {
	fakeRepository
	calls chan int
	count atomic.Int64
}

func (r *periodicRecoveryRepository) RecoverStaleTasks(context.Context) (int64, error) {
	n := int(r.count.Add(1))
	r.calls <- n
	if n == 1 {
		return 0, errors.New("temporary database failure")
	}
	return 1, nil
}

func TestPoolLimitsConcurrencyAndStops(t *testing.T) {
	repo := newQueueRepository(6)
	release := make(chan struct{})
	speechClient := &countingSpeech{release: release, entered: make(chan struct{}, 6)}
	config := testConfig()
	config.Workers = 2
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(repo, speechClient, staticLLM{summary: "summary"}, config, nil).Run(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case <-speechClient.entered:
		case <-time.After(time.Second):
			t.Fatal("workers did not start")
		}
	}
	select {
	case <-speechClient.entered:
		t.Fatal("more than two tasks entered speech concurrently")
	case <-time.After(20 * time.Millisecond):
	}
	if got := speechClient.maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrency = %d", got)
	}
	close(release)
	repo.waitCompleted(t, 6)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool did not stop")
	}
	if repo.duplicate {
		t.Fatal("a task was claimed more than once")
	}
}

func testConfig() Config {
	return Config{Workers: 1, PollInterval: time.Millisecond, SpeechTimeout: time.Second, LLMTimeout: time.Second, ProcessingTimeout: 2 * time.Second, CleanupTimeout: time.Second}
}

type staticSpeech struct {
	text string
	err  error
}

func (s staticSpeech) Transcribe(context.Context, speech.Request) (string, error) {
	return s.text, s.err
}

type blockingSpeech struct{}

func (blockingSpeech) Transcribe(ctx context.Context, _ speech.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type staticLLM struct {
	summary string
	err     error
}

func (s staticLLM) Summarize(context.Context, string) (string, error)      { return s.summary, s.err }
func (s staticLLM) Answer(context.Context, string, string) (string, error) { return "", nil }

type fakeRepository struct {
	transcript string
	summary    string
	failed     string
	requeued   string
}

func (*fakeRepository) ClaimTask(context.Context, time.Duration) (domain.ClaimedTask, bool, error) {
	return domain.ClaimedTask{}, false, nil
}
func (f *fakeRepository) SaveTranscript(_ context.Context, _, _, _ int64, text string) error {
	f.transcript = text
	return nil
}
func (f *fakeRepository) CompleteTask(_ context.Context, _, _, _ int64, summary string) error {
	f.summary = summary
	return nil
}
func (f *fakeRepository) FailTask(_ context.Context, _, _ int64, message string) error {
	f.failed = message
	return nil
}
func (f *fakeRepository) RequeueTask(_ context.Context, _, _ int64, message string) error {
	f.requeued = message
	return nil
}
func (*fakeRepository) RecoverStaleTasks(context.Context) (int64, error) { return 0, nil }

type countingSpeech struct {
	release chan struct{}
	entered chan struct{}
	active  atomic.Int64
	maximum atomic.Int64
}

func (s *countingSpeech) Transcribe(ctx context.Context, _ speech.Request) (string, error) {
	active := s.active.Add(1)
	for {
		maximum := s.maximum.Load()
		if active <= maximum || s.maximum.CompareAndSwap(maximum, active) {
			break
		}
	}
	s.entered <- struct{}{}
	select {
	case <-s.release:
		s.active.Add(-1)
		return "text", nil
	case <-ctx.Done():
		s.active.Add(-1)
		return "", ctx.Err()
	}
}

type queueRepository struct {
	mu        sync.Mutex
	queue     []domain.ClaimedTask
	claimed   map[int64]bool
	completed int
	done      chan struct{}
	duplicate bool
}

func newQueueRepository(count int) *queueRepository {
	repo := &queueRepository{claimed: make(map[int64]bool), done: make(chan struct{}, count)}
	for id := 1; id <= count; id++ {
		repo.queue = append(repo.queue, domain.ClaimedTask{TaskID: int64(id), MeetingID: int64(id), FileFormat: "txt"})
	}
	return repo
}

func (r *queueRepository) ClaimTask(context.Context, time.Duration) (domain.ClaimedTask, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return domain.ClaimedTask{}, false, nil
	}
	task := r.queue[0]
	r.queue = r.queue[1:]
	if r.claimed[task.TaskID] {
		r.duplicate = true
	}
	r.claimed[task.TaskID] = true
	return task, true, nil
}
func (*queueRepository) SaveTranscript(context.Context, int64, int64, int64, string) error {
	return nil
}
func (r *queueRepository) CompleteTask(context.Context, int64, int64, int64, string) error {
	r.mu.Lock()
	r.completed++
	r.mu.Unlock()
	r.done <- struct{}{}
	return nil
}
func (*queueRepository) FailTask(context.Context, int64, int64, string) error    { return nil }
func (*queueRepository) RequeueTask(context.Context, int64, int64, string) error { return nil }
func (*queueRepository) RecoverStaleTasks(context.Context) (int64, error)        { return 0, nil }
func (r *queueRepository) waitCompleted(t *testing.T, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-r.done:
		case <-time.After(time.Second):
			t.Fatalf("completed %d of %d tasks", i, want)
		}
	}
}
