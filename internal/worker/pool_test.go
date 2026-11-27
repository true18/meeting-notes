package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/llm"
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
	synctest.Test(t, func(t *testing.T) {
		repo := &periodicRecoveryRepository{calls: make(chan int, 8)}
		pool := New(repo, staticSpeech{}, staticLLM{}, testConfig(), nil)
		pool.config.RecoveryInterval = time.Second
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { pool.recoverPeriodically(ctx); close(done) }()

		synctest.Wait()
		if len(repo.calls) != 0 {
			t.Fatal("recovery ran before its interval")
		}
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if len(repo.calls) != 0 {
			t.Fatal("recovery ran before its interval")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := <-repo.calls; got != 1 {
			t.Fatalf("recovery call = %d, want 1", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := <-repo.calls; got != 2 {
			t.Fatalf("recovery call = %d, want 2 after a temporary error", got)
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("recovery goroutine did not stop")
		}
	})
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
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers=%d", workers), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := newQueueRepository(6)
				release := make(chan struct{})
				speechClient := &countingSpeech{release: release, entered: make(chan struct{}, 6)}
				config := testConfig()
				config.Workers = workers
				config.RecoveryInterval = time.Hour
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- New(repo, speechClient, staticLLM{summary: "summary"}, config, nil).Run(ctx) }()

				for i := 0; i < workers; i++ {
					<-speechClient.entered
				}
				synctest.Wait()
				if got := repo.claimCount(); got != workers {
					t.Fatalf("claimed %d tasks while all workers were occupied, want %d", got, workers)
				}
				if got := speechClient.maximum.Load(); got != int64(workers) {
					t.Fatalf("maximum concurrency = %d, want %d", got, workers)
				}

				close(release)
				for range 6 {
					<-repo.done
				}
				synctest.Wait()
				cancel()
				synctest.Wait()
				if err := <-done; err != nil {
					t.Fatalf("Run: %v", err)
				}
				if repo.duplicate {
					t.Fatal("a task was claimed more than once")
				}
			})
		})
	}
}

func TestPoolPollsForNewTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newQueueRepository(0)
		config := testConfig()
		config.PollInterval = time.Second
		config.RecoveryInterval = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- New(repo, staticSpeech{text: "transcript"}, staticLLM{summary: "summary"}, config, nil).Run(ctx)
		}()

		synctest.Wait()
		if got := repo.claimCount(); got != 1 {
			t.Fatalf("initial claim count = %d, want 1", got)
		}
		repo.addTask(domain.ClaimedTask{TaskID: 1, MeetingID: 1, FileFormat: "txt"})
		time.Sleep(config.PollInterval)
		synctest.Wait()
		<-repo.done
		if got := repo.claimCount(); got < 2 {
			t.Fatalf("new task was not claimed after polling: claims=%d", got)
		}

		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
	})
}

func TestPoolContinuesAfterTaskErrors(t *testing.T) {
	for _, failure := range []string{"speech", "LLM"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := newQueueRepository(2)
				repo.queue[0].MeetingName = failure + "-error"
				repo.queue[1].MeetingName = "ok"
				var speechClient speech.Client = namedSpeech{}
				var llmClient llm.Client = staticLLM{summary: "summary"}
				if failure == "speech" {
					speechClient = selectiveSpeech{failName: "speech-error"}
				} else {
					llmClient = selectiveLLM{failText: "LLM-error"}
				}
				config := testConfig()
				config.PollInterval = time.Second
				config.RecoveryInterval = time.Hour
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() { done <- New(repo, speechClient, llmClient, config, nil).Run(ctx) }()

				<-repo.failed
				<-repo.done
				synctest.Wait()
				if got := repo.claimCount(); got < 2 {
					t.Fatalf("worker stopped after a task error; claims=%d", got)
				}
				cancel()
				synctest.Wait()
				if err := <-done; err != nil {
					t.Fatalf("Run: %v", err)
				}
			})
		})
	}
}

func TestPoolShutdownRequeuesActiveTasks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newQueueRepository(2)
		release := make(chan struct{})
		speechClient := &countingSpeech{release: release, entered: make(chan struct{}, 2)}
		config := testConfig()
		config.Workers = 2
		config.RecoveryInterval = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- New(repo, speechClient, staticLLM{}, config, nil).Run(ctx) }()
		<-speechClient.entered
		<-speechClient.entered
		synctest.Wait()
		cancel()
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatalf("Run: %v", err)
		}
		if got := len(repo.requeued); got != 2 {
			t.Fatalf("requeued %d active tasks, want 2", got)
		}
	})
}

func TestProcessingTimeoutUsesVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := &failureSignalRepository{failed: make(chan string, 1)}
		config := testConfig()
		config.ProcessingTimeout = time.Second
		pool := New(repo, timeoutSpeech{}, staticLLM{}, config, nil)
		done := make(chan struct{})
		go func() {
			pool.process(context.Background(), 1, domain.ClaimedTask{TaskID: 1, MeetingID: 1})
			close(done)
		}()
		select {
		case message := <-repo.failed:
			t.Fatalf("task failed before timeout: %s", message)
		default:
		}
		time.Sleep(config.ProcessingTimeout)
		synctest.Wait()
		<-done
		select {
		case <-repo.failed:
		default:
			t.Fatal("processing timeout was not recorded")
		}
	})
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

type timeoutSpeech struct{}

func (s timeoutSpeech) Transcribe(ctx context.Context, _ speech.Request) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

type namedSpeech struct{}

func (namedSpeech) Transcribe(_ context.Context, request speech.Request) (string, error) {
	return request.Name, nil
}

type selectiveSpeech struct{ failName string }

func (s selectiveSpeech) Transcribe(_ context.Context, request speech.Request) (string, error) {
	if request.Name == s.failName {
		return "", errors.New("speech unavailable")
	}
	return request.Name, nil
}

type selectiveLLM struct{ failText string }

func (c selectiveLLM) Summarize(_ context.Context, text string) (string, error) {
	if text == c.failText {
		return "", errors.New("LLM unavailable")
	}
	return "summary", nil
}

func (selectiveLLM) Answer(context.Context, string, string) (string, error) { return "", nil }

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

type failureSignalRepository struct {
	fakeRepository
	failed chan string
}

func (r *failureSignalRepository) FailTask(_ context.Context, _, _ int64, message string) error {
	r.failed <- message
	return nil
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
	claims    int
	done      chan struct{}
	failed    chan string
	requeued  chan int64
	duplicate bool
}

func newQueueRepository(count int) *queueRepository {
	repo := &queueRepository{claimed: make(map[int64]bool), done: make(chan struct{}, count), failed: make(chan string, count), requeued: make(chan int64, count)}
	for id := 1; id <= count; id++ {
		repo.queue = append(repo.queue, domain.ClaimedTask{TaskID: int64(id), MeetingID: int64(id), FileFormat: "txt"})
	}
	return repo
}

func (r *queueRepository) ClaimTask(context.Context, time.Duration) (domain.ClaimedTask, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.claims++
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
func (r *queueRepository) addTask(task domain.ClaimedTask) {
	r.mu.Lock()
	r.queue = append(r.queue, task)
	r.mu.Unlock()
}
func (r *queueRepository) claimCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.claims
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
func (r *queueRepository) FailTask(_ context.Context, _ int64, _ int64, message string) error {
	r.failed <- message
	return nil
}
func (r *queueRepository) RequeueTask(_ context.Context, taskID, _ int64, _ string) error {
	r.requeued <- taskID
	return nil
}
func (*queueRepository) RecoverStaleTasks(context.Context) (int64, error) { return 0, nil }
