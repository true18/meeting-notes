package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"meeting-notes/internal/domain"
	"meeting-notes/internal/repository/postgres"
	"meeting-notes/migrations"
)

func TestPostgresLifecycleAndIsolation(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	alice, err := store.EnsureUser(ctx, "alice")
	if err != nil {
		t.Fatalf("EnsureUser alice: %v", err)
	}
	aliceAgain, err := store.EnsureUser(ctx, "alice")
	if err != nil || aliceAgain.ID != alice.ID {
		t.Fatalf("EnsureUser idempotency = %+v, %v", aliceAgain, err)
	}
	bob, err := store.EnsureUser(ctx, "bob")
	if err != nil {
		t.Fatalf("EnsureUser bob: %v", err)
	}

	aliceMeeting, err := store.CreateMeetingWithTask(ctx, alice.ID, "release.txt", "/uploads/release.txt", "txt")
	if err != nil {
		t.Fatalf("CreateMeetingWithTask alice: %v", err)
	}
	if aliceMeeting.Status != domain.StatusCreated || aliceMeeting.TaskID == 0 {
		t.Fatalf("unexpected task: %+v", aliceMeeting)
	}
	if _, err := store.CreateMeetingWithTask(ctx, -999, "invalid.txt", "/uploads/invalid.txt", "txt"); err == nil {
		t.Fatal("foreign-key transaction returned nil error")
	}
	var aliceCount int
	if err := store.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM meetings WHERE user_id=$1`, alice.ID).Scan(&aliceCount); err != nil || aliceCount != 1 {
		t.Fatalf("meeting transaction left partial data: count=%d err=%v", aliceCount, err)
	}

	bobMeeting, err := store.CreateMeetingWithTask(ctx, bob.ID, "planning.txt", "/uploads/planning.txt", "txt")
	if err != nil {
		t.Fatalf("CreateMeetingWithTask bob: %v", err)
	}
	claimed := claimByMeeting(t, ctx, store, map[int64]bool{aliceMeeting.ID: true, bobMeeting.ID: true})
	aliceTask := claimed[aliceMeeting.ID]
	bobTask := claimed[bobMeeting.ID]
	if err := store.SaveTranscript(ctx, aliceTask.TaskID, aliceMeeting.ID, "Обсудили релиз. Алексей отвечает за тестирование."); err != nil {
		t.Fatalf("SaveTranscript alice: %v", err)
	}
	if err := store.CompleteTask(ctx, aliceTask.TaskID, aliceMeeting.ID, "Релиз и тестирование согласованы."); err != nil {
		t.Fatalf("CompleteTask alice: %v", err)
	}
	if err := store.SaveTranscript(ctx, bobTask.TaskID, bobMeeting.ID, "Обсудили бюджет клиента."); err != nil {
		t.Fatalf("SaveTranscript bob: %v", err)
	}
	if err := store.CompleteTask(ctx, bobTask.TaskID, bobMeeting.ID, "Бюджет обсуждён."); err != nil {
		t.Fatalf("CompleteTask bob: %v", err)
	}

	transcript, err := store.GetTranscript(ctx, alice.ID, aliceMeeting.ID)
	if err != nil || transcript == "" {
		t.Fatalf("GetTranscript alice = %q, %v", transcript, err)
	}
	if _, err := store.GetTranscript(ctx, bob.ID, aliceMeeting.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("bob read alice transcript: %v", err)
	}
	if _, err := store.GetMeeting(ctx, bob.ID, aliceMeeting.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("bob read alice meeting: %v", err)
	}
	list, err := store.ListMeetings(ctx, alice.ID)
	if err != nil || len(list) != 1 || list[0].ID != aliceMeeting.ID || list[0].Status != domain.StatusCompleted {
		t.Fatalf("ListMeetings alice = %+v, %v", list, err)
	}
	results, err := store.Search(ctx, alice.ID, "релиз", 10)
	if err != nil || len(results) != 1 || results[0].MeetingID != aliceMeeting.ID {
		t.Fatalf("Search alice = %+v, %v", results, err)
	}
	results, err = store.Search(ctx, bob.ID, "релиз", 10)
	if err != nil || len(results) != 0 {
		t.Fatalf("Search bob leaked data = %+v, %v", results, err)
	}
	parts, err := store.ChatContext(ctx, alice.ID, &aliceMeeting.ID, 5)
	if err != nil || len(parts) != 1 {
		t.Fatalf("ChatContext alice = %+v, %v", parts, err)
	}
	if _, err := store.ChatContext(ctx, bob.ID, &aliceMeeting.ID, 5); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("bob chat on alice meeting: %v", err)
	}

	failedMeeting, err := store.CreateMeetingWithTask(ctx, alice.ID, "failure.txt", "/uploads/failure.txt", "txt")
	if err != nil {
		t.Fatal(err)
	}
	failedTask, found, err := store.ClaimTask(ctx)
	if err != nil || !found || failedTask.MeetingID != failedMeeting.ID {
		t.Fatalf("ClaimTask failed scenario = %+v, %t, %v", failedTask, found, err)
	}
	if err := store.FailTask(ctx, failedTask.TaskID, "speech unavailable"); err != nil {
		t.Fatal(err)
	}
	status, err := store.GetMeeting(ctx, alice.ID, failedMeeting.ID)
	if err != nil || status.Status != domain.StatusFailed || status.ErrorMessage != "speech unavailable" {
		t.Fatalf("failed status = %+v, %v", status, err)
	}
	if err := store.RetryTask(ctx, alice.ID, failedMeeting.ID); err != nil {
		t.Fatal(err)
	}
	status, err = store.GetMeeting(ctx, alice.ID, failedMeeting.ID)
	if err != nil || status.Status != domain.StatusCreated || status.ErrorMessage != "speech unavailable" {
		t.Fatalf("retry status = %+v, %v", status, err)
	}
}

func TestPostgresConcurrentClaimsAreUnique(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user, err := store.EnsureUser(ctx, "concurrent")
	if err != nil {
		t.Fatal(err)
	}
	const count = 8
	for i := 0; i < count; i++ {
		if _, err := store.CreateMeetingWithTask(ctx, user.ID, fmt.Sprintf("meeting-%d.txt", i), fmt.Sprintf("/uploads/%d.txt", i), "txt"); err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	ids := make(chan int64, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			task, found, err := store.ClaimTask(ctx)
			if err != nil {
				errs <- err
				return
			}
			if !found {
				errs <- errors.New("no task claimed")
				return
			}
			ids <- task.TaskID
		}()
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("ClaimTask: %v", err)
	}
	seen := make(map[int64]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("task %d claimed twice", id)
		}
		seen[id] = true
	}
	if len(seen) != count {
		t.Fatalf("claimed %d tasks, want %d", len(seen), count)
	}
}

func claimByMeeting(t *testing.T, ctx context.Context, store *postgres.Store, meetings map[int64]bool) map[int64]domain.ClaimedTask {
	t.Helper()
	claimed := make(map[int64]domain.ClaimedTask)
	for range meetings {
		task, found, err := store.ClaimTask(ctx)
		if err != nil || !found {
			t.Fatalf("ClaimTask = %+v, %t, %v", task, found, err)
		}
		claimed[task.MeetingID] = task
	}
	return claimed
}

func integrationStore(t *testing.T) *postgres.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("TEST_DATABASE_DSN is not set; PostgreSQL integration test skipped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	schema := fmt.Sprintf("meeting_notes_test_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatalf("create test schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
		admin.Close()
	})
	return postgres.New(pool)
}
