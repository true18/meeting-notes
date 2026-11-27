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

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/repository/postgres"
	"github.com/true18/meeting-notes/migrations"
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
	if err := store.SaveTranscript(ctx, aliceTask.TaskID, aliceTask.Attempt, aliceMeeting.ID, "Обсудили подготовку релиза. Алексей отвечает за тестирование."); err != nil {
		t.Fatalf("SaveTranscript alice: %v", err)
	}
	if err := store.CompleteTask(ctx, aliceTask.TaskID, aliceTask.Attempt, aliceMeeting.ID, "Релиз и тестирование согласованы."); err != nil {
		t.Fatalf("CompleteTask alice: %v", err)
	}
	if err := store.SaveTranscript(ctx, bobTask.TaskID, bobTask.Attempt, bobMeeting.ID, "Обсудили бюджет клиента."); err != nil {
		t.Fatalf("SaveTranscript bob: %v", err)
	}
	if err := store.CompleteTask(ctx, bobTask.TaskID, bobTask.Attempt, bobMeeting.ID, "Бюджет обсуждён."); err != nil {
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
	failedTask, found, err := store.ClaimTask(ctx, time.Minute)
	if err != nil || !found || failedTask.MeetingID != failedMeeting.ID {
		t.Fatalf("ClaimTask failed scenario = %+v, %t, %v", failedTask, found, err)
	}
	if err := store.FailTask(ctx, failedTask.TaskID, failedTask.Attempt, "speech unavailable"); err != nil {
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
			task, found, err := store.ClaimTask(ctx, time.Minute)
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

func TestPostgresRecoveryFencesExpiredAttempt(t *testing.T) {
	store := integrationStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user, err := store.EnsureUser(ctx, "fencing")
	if err != nil {
		t.Fatal(err)
	}
	activeMeeting, err := store.CreateMeetingWithTask(ctx, user.ID, "active.txt", "/uploads/active.txt", "txt")
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := store.ClaimTask(ctx, time.Minute)
	if err != nil || !found {
		t.Fatalf("claim active = %v, %v", found, err)
	}
	if recovered, err := store.RecoverStaleTasks(ctx); err != nil || recovered != 0 {
		t.Fatalf("recovery touched active lease: recovered=%d err=%v", recovered, err)
	}

	staleMeeting, err := store.CreateMeetingWithTask(ctx, user.ID, "stale.txt", "/uploads/stale.txt", "txt")
	if err != nil {
		t.Fatal(err)
	}
	first, found, err := store.ClaimTask(ctx, time.Minute)
	if err != nil || !found || first.MeetingID != staleMeeting.ID {
		t.Fatalf("claim first = %+v %v %v", first, found, err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE processing_tasks SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, first.TaskID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan int64, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := store.RecoverStaleTasks(ctx)
			results <- n
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	var total int64
	for n := range results {
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent recovery: %v", err)
		}
	}
	if total != 1 {
		t.Fatalf("recovered rows = %d, want 1", total)
	}
	if n, err := store.RecoverStaleTasks(ctx); err != nil || n != 0 {
		t.Fatalf("repeat recovery = %d, %v", n, err)
	}
	second, found, err := store.ClaimTask(ctx, time.Minute)
	if err != nil || !found || second.TaskID != first.TaskID || second.Attempt != first.Attempt+1 {
		t.Fatalf("claim second = %+v found=%v err=%v", second, found, err)
	}

	if err := store.SaveTranscript(ctx, first.TaskID, first.Attempt, first.MeetingID, "stale transcript"); err == nil {
		t.Fatal("stale transcript accepted")
	}
	if err := store.CompleteTask(ctx, first.TaskID, first.Attempt, first.MeetingID, "stale summary"); err == nil {
		t.Fatal("stale summary accepted")
	}
	if err := store.FailTask(ctx, first.TaskID, first.Attempt, "stale failure"); err != nil {
		t.Fatal(err)
	}
	if err := store.RequeueTask(ctx, first.TaskID, first.Attempt, "stale requeue"); err != nil {
		t.Fatal(err)
	}
	var status string
	var transcriptCount int
	if err := store.Pool().QueryRow(ctx, `SELECT status FROM processing_tasks WHERE id=$1`, first.TaskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT count(*) FROM transcripts WHERE meeting_id=$1`, first.MeetingID).Scan(&transcriptCount); err != nil {
		t.Fatal(err)
	}
	if status != string(domain.StatusProcessing) || transcriptCount != 0 {
		t.Fatalf("stale attempt changed task: status=%s transcripts=%d", status, transcriptCount)
	}
	if err := store.SaveTranscript(ctx, second.TaskID, second.Attempt, second.MeetingID, "current transcript"); err != nil {
		t.Fatalf("save current transcript: %v", err)
	}
	if err := store.CompleteTask(ctx, second.TaskID, second.Attempt, second.MeetingID, "current summary"); err != nil {
		t.Fatalf("complete current attempt: %v", err)
	}
	var savedTranscript, savedSummary string
	if err := store.Pool().QueryRow(ctx, `SELECT text FROM transcripts WHERE meeting_id=$1`, second.MeetingID).Scan(&savedTranscript); err != nil {
		t.Fatal(err)
	}
	if err := store.Pool().QueryRow(ctx, `SELECT text FROM summaries WHERE meeting_id=$1`, second.MeetingID).Scan(&savedSummary); err != nil {
		t.Fatal(err)
	}
	if savedTranscript != "current transcript" || savedSummary != "current summary" {
		t.Fatalf("saved stale results: transcript=%q summary=%q", savedTranscript, savedSummary)
	}
	terminal, err := store.CreateMeetingWithTask(ctx, user.ID, "terminal.txt", "/uploads/terminal.txt", "txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool().Exec(ctx, `UPDATE processing_tasks SET status=CASE WHEN meeting_id=$1 THEN 'failed' ELSE 'completed' END, lease_expires_at=NOW()-INTERVAL '1 second' WHERE meeting_id IN ($1,$2)`, terminal.ID, activeMeeting.ID); err != nil {
		t.Fatal(err)
	}
	if recovered, err := store.RecoverStaleTasks(ctx); err != nil || recovered != 0 {
		t.Fatalf("recovery changed terminal tasks: %d, %v", recovered, err)
	}
	var terminalStatuses []string
	rows, err := store.Pool().Query(ctx, `SELECT status FROM processing_tasks WHERE meeting_id IN ($1,$2) ORDER BY meeting_id`, terminal.ID, activeMeeting.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		terminalStatuses = append(terminalStatuses, value)
	}
	rows.Close()
	if len(terminalStatuses) != 2 || terminalStatuses[0] != string(domain.StatusCompleted) || terminalStatuses[1] != string(domain.StatusFailed) {
		t.Fatalf("terminal statuses changed: %v", terminalStatuses)
	}
	_ = active
}

func claimByMeeting(t *testing.T, ctx context.Context, store *postgres.Store, meetings map[int64]bool) map[int64]domain.ClaimedTask {
	t.Helper()
	claimed := make(map[int64]domain.ClaimedTask)
	for range meetings {
		task, found, err := store.ClaimTask(ctx, time.Minute)
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
