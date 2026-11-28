package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/repository/postgres"
	"github.com/true18/meeting-notes/internal/speech"
	"github.com/true18/meeting-notes/internal/worker"
	"github.com/true18/meeting-notes/migrations"
)

const toxiproxyListenPort = 18666

func TestToxiproxyPostgresResilience(t *testing.T) {
	apiURL := os.Getenv("TEST_TOXIPROXY_URL")
	dsn := os.Getenv("TEST_TOXIPROXY_DSN")
	if apiURL == "" || dsn == "" {
		t.Skip("TEST_TOXIPROXY_URL and TEST_TOXIPROXY_DSN are required; Toxiproxy integration test skipped")
	}
	if err := validateLoopbackURL(apiURL); err != nil {
		t.Fatalf("TEST_TOXIPROXY_URL must be a local HTTP endpoint: %v", err)
	}
	dbConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_TOXIPROXY_DSN: %v", err)
	}
	if !isLoopbackHost(dbConfig.ConnConfig.Host) || dbConfig.ConnConfig.Port != toxiproxyListenPort {
		t.Fatalf("TEST_TOXIPROXY_DSN must target local port %d", toxiproxyListenPort)
	}

	api := &toxiproxyAPI{baseURL: strings.TrimRight(apiURL, "/"), client: &http.Client{Timeout: 3 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := api.waitReady(ctx); err != nil {
		t.Fatalf("Toxiproxy API is unavailable: %v", err)
	}
	name := fmt.Sprintf("meeting_notes_it_%d", time.Now().UnixNano())
	if err := api.createProxy(name); err != nil {
		t.Fatalf("create test proxy: %v", err)
	}

	var pools []*pgxpool.Pool
	var admin *pgxpool.Pool
	var schema string
	var toxicNames []string
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, toxicName := range toxicNames {
			if err := api.deleteToxic(name, toxicName); err != nil {
				t.Errorf("remove toxic %s during cleanup: %v", toxicName, err)
			}
		}
		_ = api.setProxyEnabled(name, true)
		for _, pool := range pools {
			pool.Close()
		}
		if admin != nil {
			if schema != "" {
				if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
					t.Errorf("drop isolated Toxiproxy test schema: %v", err)
				}
			}
			admin.Close()
		}
		if err := api.deleteProxy(name); err != nil {
			t.Errorf("delete test proxy: %v", err)
		}
	})

	admin, err = pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open isolated test database through proxy: %v", err)
	}
	if err := admin.Ping(ctx); err != nil {
		t.Fatalf("ping isolated test database through proxy: %v", err)
	}
	schema = fmt.Sprintf("meeting_notes_toxiproxy_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create isolated test schema: %v", err)
	}

	appConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if appConfig.ConnConfig.RuntimeParams == nil {
		appConfig.ConnConfig.RuntimeParams = make(map[string]string)
	}
	appConfig.ConnConfig.RuntimeParams["search_path"] = schema
	appConfig.MinConns = 1
	appConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatalf("open repository pool through proxy: %v", err)
	}
	pools = append(pools, pool)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("warm repository pool: %v", err)
	}
	if err := migrations.Apply(ctx, pool); err != nil {
		t.Fatalf("apply migrations in isolated schema: %v", err)
	}
	store := postgres.New(pool)

	if !t.Run("normal repository query", func(t *testing.T) {
		first, err := store.EnsureUser(ctx, "toxiproxy-normal")
		if err != nil {
			t.Fatalf("EnsureUser through proxy: %v", err)
		}
		again, err := store.EnsureUser(ctx, "toxiproxy-normal")
		if err != nil || again.ID != first.ID {
			t.Fatalf("repeat EnsureUser = %+v, %v", again, err)
		}
	}) {
		return
	}

	if !t.Run("deadline under latency and pool recovery", func(t *testing.T) {
		toxicName := fmt.Sprintf("latency_%d", time.Now().UnixNano())
		if err := api.addToxic(name, toxicName, "latency", "downstream", map[string]int{"latency": 2000, "jitter": 0}); err != nil {
			t.Fatalf("add 2000ms latency toxic: %v", err)
		}
		toxicNames = append(toxicNames, toxicName)

		queryCtx, queryCancel := context.WithTimeout(ctx, 300*time.Millisecond)
		started := time.Now()
		_, queryErr := store.EnsureUser(queryCtx, "toxiproxy-latency-deadline")
		elapsed := time.Since(started)
		queryCancel()
		t.Logf("repository query with 2000ms downstream latency and 300ms deadline: %s, error=%v", elapsed, queryErr)
		if !errors.Is(queryErr, context.DeadlineExceeded) {
			t.Fatalf("repository error = %v, want context deadline exceeded", queryErr)
		}
		if elapsed >= 1500*time.Millisecond {
			t.Fatalf("query took %s despite 300ms deadline and 2000ms toxic latency", elapsed)
		}
		if err := removeTrackedToxic(api, name, toxicName, &toxicNames); err != nil {
			t.Fatalf("remove latency toxic: %v", err)
		}

		recoverCtx, recoverCancel := context.WithTimeout(ctx, 5*time.Second)
		defer recoverCancel()
		if _, err := store.EnsureUser(recoverCtx, "toxiproxy-after-latency"); err != nil {
			t.Fatalf("same pgxpool did not recover after latency: %v", err)
		}
	}) {
		return
	}

	if !t.Run("reset peer and pool recovery", func(t *testing.T) {
		resetConfig, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse reset-test pool config: %v", err)
		}
		if resetConfig.ConnConfig.RuntimeParams == nil {
			resetConfig.ConnConfig.RuntimeParams = make(map[string]string)
		}
		resetConfig.ConnConfig.RuntimeParams["search_path"] = schema
		resetConfig.MinConns = 0
		resetConfig.MaxConns = 1
		resetPool, err := pgxpool.NewWithConfig(ctx, resetConfig)
		if err != nil {
			t.Fatalf("open lazy reset-test pool: %v", err)
		}
		pools = append(pools, resetPool)
		if got := resetPool.Stat().TotalConns(); got != 0 {
			t.Fatalf("reset-test pool has %d connections before fault injection", got)
		}
		resetName := fmt.Sprintf("reset_%d", time.Now().UnixNano())
		if err := api.addToxic(name, resetName, "reset_peer", "downstream", map[string]int{"timeout": 0}); err != nil {
			t.Fatalf("add reset_peer toxic: %v", err)
		}
		toxicNames = append(toxicNames, resetName)
		resetStore := postgres.New(resetPool)
		resetCtx, resetCancel := context.WithTimeout(ctx, 3*time.Second)
		started := time.Now()
		_, resetErr := resetStore.EnsureUser(resetCtx, "toxiproxy-reset-peer")
		elapsed := time.Since(started)
		resetCancel()
		t.Logf("repository query through reset_peer: %s, error=%v", elapsed, resetErr)
		if resetErr == nil {
			t.Fatal("repository query unexpectedly succeeded during reset_peer")
		}
		if elapsed >= 2*time.Second {
			t.Fatalf("reset_peer did not promptly break the connection: %s", elapsed)
		}
		if err := removeTrackedToxic(api, name, resetName, &toxicNames); err != nil {
			t.Fatalf("remove reset_peer toxic: %v", err)
		}
		recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 5*time.Second)
		defer recoveryCancel()
		if _, err := resetStore.EnsureUser(recoveryCtx, "toxiproxy-after-reset"); err != nil {
			t.Fatalf("same pgxpool did not recover after reset_peer: %v", err)
		}
	}) {
		return
	}

	t.Run("worker recovers a fenced task after temporary outage", func(t *testing.T) {
		user, err := store.EnsureUser(ctx, "toxiproxy-worker")
		if err != nil {
			t.Fatal(err)
		}
		meeting, err := store.CreateMeetingWithTask(ctx, user.ID, "outage.txt", "/uploads/outage.txt", "txt")
		if err != nil {
			t.Fatal(err)
		}

		speechClient := &outageSpeech{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{}), attempts: make(chan int, 4)}
		observed := &outageRepository{Repository: store, saveErrors: make(chan error, 2), failErrors: make(chan error, 2)}
		workerConfig := worker.Config{
			Workers: 1, PollInterval: 50 * time.Millisecond, RecoveryInterval: 100 * time.Millisecond,
			SpeechTimeout: 5 * time.Second, LLMTimeout: 2 * time.Second,
			ProcessingTimeout: 2 * time.Second, CleanupTimeout: time.Second,
		}
		workerCtx, stopWorker := context.WithCancel(context.Background())
		workerDone := make(chan error, 1)
		go func() {
			workerDone <- worker.New(observed, speechClient, outageLLM{}, workerConfig, nil).Run(workerCtx)
		}()
		workerStopped := false
		defer func() {
			stopWorker()
			if !workerStopped {
				select {
				case err := <-workerDone:
					if err != nil {
						t.Errorf("worker shutdown: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("worker did not stop")
				}
			}
		}()
		select {
		case <-speechClient.firstStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not start the first attempt")
		}

		if err := api.setProxyEnabled(name, false); err != nil {
			t.Fatalf("disable proxy for worker outage: %v", err)
		}
		close(speechClient.releaseFirst)
		select {
		case err := <-observed.saveErrors:
			if err == nil {
				t.Fatal("SaveTranscript unexpectedly succeeded during outage")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("did not observe repository failure while proxy was disabled")
		}
		select {
		case err := <-observed.failErrors:
			if err == nil {
				t.Fatal("FailTask unexpectedly succeeded during outage")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("did not observe failed status persistence during outage")
		}
		if err := api.setProxyEnabled(name, true); err != nil {
			t.Fatalf("reenable proxy: %v", err)
		}

		select {
		case attempt := <-speechClient.attempts:
			if attempt != 1 {
				t.Fatalf("first speech attempt = %d", attempt)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing first speech attempt")
		}
		select {
		case attempt := <-speechClient.attempts:
			if attempt != 2 {
				t.Fatalf("recovered speech attempt = %d, want 2", attempt)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("periodic recovery did not restart the expired task")
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 5*time.Second)
		defer waitCancel()
		waitForMeetingStatus(t, waitCtx, store, user.ID, meeting.ID, domain.StatusCompleted)

		var attempt int64
		var savedTranscript, status string
		if err := pool.QueryRow(ctx, `SELECT t.attempt, t.status, tr.text FROM processing_tasks t JOIN transcripts tr ON tr.meeting_id=t.meeting_id WHERE t.meeting_id=$1`, meeting.ID).Scan(&attempt, &status, &savedTranscript); err != nil {
			t.Fatalf("read recovered task result: %v", err)
		}
		if attempt != 2 || status != string(domain.StatusCompleted) || savedTranscript != "result from recovered attempt" {
			t.Fatalf("recovered task state: attempt=%d status=%s transcript=%q", attempt, status, savedTranscript)
		}
		if err := store.SaveTranscript(ctx, meeting.TaskID, 1, meeting.ID, "stale result"); err == nil {
			t.Fatal("stale attempt wrote a transcript after recovery")
		}
		if err := store.CompleteTask(ctx, meeting.TaskID, 1, meeting.ID, "stale summary"); err == nil {
			t.Fatal("stale attempt completed the recovered task")
		}
		var finalTranscript, finalSummary string
		if err := pool.QueryRow(ctx, `SELECT text FROM transcripts WHERE meeting_id=$1`, meeting.ID).Scan(&finalTranscript); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT text FROM summaries WHERE meeting_id=$1`, meeting.ID).Scan(&finalSummary); err != nil {
			t.Fatal(err)
		}
		if finalTranscript != "result from recovered attempt" || finalSummary != "summary" {
			t.Fatalf("stale attempt changed recovered result: transcript=%q summary=%q", finalTranscript, finalSummary)
		}
		stopWorker()
		select {
		case err := <-workerDone:
			if err != nil {
				t.Fatalf("worker Run: %v", err)
			}
			workerStopped = true
		case <-time.After(5 * time.Second):
			t.Fatal("worker shutdown timed out")
		}
	})
}

func waitForMeetingStatus(t *testing.T, ctx context.Context, store *postgres.Store, userID, meetingID int64, want domain.TaskStatus) {
	t.Helper()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		meeting, err := store.GetMeeting(ctx, userID, meetingID)
		if err == nil && meeting.Status == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("meeting status did not reach %s: last=%s err=%v", want, meeting.Status, err)
		case <-ticker.C:
		}
	}
}

func validateLoopbackURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || !isLoopbackHost(u.Hostname()) {
		return errors.New("expected http URL on localhost or a loopback IP")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type toxiproxyAPI struct {
	baseURL string
	client  *http.Client
}

func (a *toxiproxyAPI) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := a.call(http.MethodGet, "/version", nil, nil); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *toxiproxyAPI) createProxy(name string) error {
	request := map[string]any{"name": name, "listen": fmt.Sprintf("0.0.0.0:%d", toxiproxyListenPort), "upstream": "postgres-toxiproxy-test:5432", "enabled": true}
	return a.call(http.MethodPost, "/proxies", request, nil)
}

func (a *toxiproxyAPI) deleteProxy(name string) error {
	return a.call(http.MethodDelete, "/proxies/"+url.PathEscape(name), nil, nil)
}

func (a *toxiproxyAPI) setProxyEnabled(name string, enabled bool) error {
	return a.call(http.MethodPost, "/proxies/"+url.PathEscape(name), map[string]bool{"enabled": enabled}, nil)
}

func (a *toxiproxyAPI) addToxic(proxy, name, kind, stream string, attributes map[string]int) error {
	request := map[string]any{"name": name, "type": kind, "stream": stream, "toxicity": 1.0, "attributes": attributes}
	return a.call(http.MethodPost, "/proxies/"+url.PathEscape(proxy)+"/toxics", request, nil)
}

func (a *toxiproxyAPI) deleteToxic(proxy, name string) error {
	return a.call(http.MethodDelete, "/proxies/"+url.PathEscape(proxy)+"/toxics/"+url.PathEscape(name), nil, nil)
}

func removeTrackedToxic(api *toxiproxyAPI, proxy, name string, active *[]string) error {
	if err := api.deleteToxic(proxy, name); err != nil {
		return err
	}
	for i, current := range *active {
		if current == name {
			*active = append((*active)[:i], (*active)[i+1:]...)
			break
		}
	}
	return nil
}

func (a *toxiproxyAPI) call(method, endpoint string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, a.baseURL+path.Clean("/"+endpoint), body)
	if err != nil {
		return err
	}
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("Toxiproxy API %s %s returned %s: %s", method, endpoint, resp.Status, strings.TrimSpace(string(message)))
	}
	if responseBody != nil {
		if err := json.NewDecoder(resp.Body).Decode(responseBody); err != nil {
			return err
		}
	}
	return nil
}

type outageSpeech struct {
	count        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
	attempts     chan int
}

func (s *outageSpeech) Transcribe(ctx context.Context, _ speech.Request) (string, error) {
	attempt := int(s.count.Add(1))
	s.attempts <- attempt
	if attempt == 1 {
		close(s.firstStarted)
		select {
		case <-s.releaseFirst:
			return "result from interrupted attempt", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return "result from recovered attempt", nil
}

type outageLLM struct{}

func (outageLLM) Summarize(context.Context, string) (string, error) { return "summary", nil }
func (outageLLM) Answer(context.Context, string, string) (string, error) {
	return "", nil
}

type outageRepository struct {
	worker.Repository
	saveErrors chan error
	failErrors chan error
}

func (r *outageRepository) SaveTranscript(ctx context.Context, taskID, attempt, meetingID int64, text string) error {
	err := r.Repository.SaveTranscript(ctx, taskID, attempt, meetingID, text)
	if err != nil {
		r.saveErrors <- err
	}
	return err
}

func (r *outageRepository) FailTask(ctx context.Context, taskID, attempt int64, message string) error {
	err := r.Repository.FailTask(ctx, taskID, attempt, message)
	if err != nil {
		r.failErrors <- err
	}
	return err
}
