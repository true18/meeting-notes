package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"meeting-notes/internal/domain"
)

func TestExecuteCommands(t *testing.T) {
	service := &fakeService{
		user:       domain.User{ID: 1, ExternalID: "alice"},
		meeting:    domain.MeetingView{Meeting: domain.Meeting{ID: 2, Title: "meeting.txt", CreatedAt: time.Unix(1, 0)}, TaskID: 3, Status: domain.StatusCreated, UpdatedAt: time.Unix(2, 0)},
		transcript: "meeting transcript",
		summary:    "meeting summary",
		answer:     "Алексей отвечает за тестирование.",
	}
	handler := Handler{Service: service, Migrate: func(context.Context) error { return nil }, RunWorker: func(context.Context) error { return nil }}
	tests := []struct {
		command  Command
		contains string
	}{
		{command: Command{Name: "start", User: "alice"}, contains: "user_id=1"},
		{command: Command{Name: "load", User: "alice", Arguments: []string{"meeting.txt"}}, contains: "meeting_id=2 task_id=3 status=created"},
		{command: Command{Name: "status", User: "alice", MeetingID: 2}, contains: "meeting_id=2"},
		{command: Command{Name: "get", User: "alice", MeetingID: 2}, contains: "meeting transcript"},
		{command: Command{Name: "summary", User: "alice", MeetingID: 2}, contains: "meeting summary"},
		{command: Command{Name: "chat", User: "alice", Arguments: []string{"-meeting", "2", "Кто отвечает?"}}, contains: "Алексей"},
		{command: Command{Name: "migrate"}, contains: "migrations applied"},
		{command: Command{Name: "worker"}, contains: "worker started"},
	}
	for _, tt := range tests {
		t.Run(tt.command.Name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := handler.Execute(context.Background(), tt.command, &stdout, &stderr); code != 0 {
				t.Fatalf("code = %d, stderr = %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.contains) {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.contains)
			}
		})
	}
}

func TestExecuteErrors(t *testing.T) {
	service := &fakeService{err: domain.ErrNotFound}
	handler := Handler{Service: service}
	var stdout, stderr bytes.Buffer
	code := handler.Execute(context.Background(), Command{Name: "get", User: "bob", MeetingID: 99}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "meeting not found") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}

	stderr.Reset()
	code = handler.Execute(context.Background(), Command{Name: "load", User: "alice"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "file path") {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
}

func TestParseChat(t *testing.T) {
	id, question, err := parseChat([]string{"-meeting", "42", "Что", "решили?"})
	if err != nil || id == nil || *id != 42 || question != "Что решили?" {
		t.Fatalf("parseChat = %v, %q, %v", id, question, err)
	}
	for _, args := range [][]string{{}, {"-meeting", "bad", "question"}} {
		if _, _, err := parseChat(args); err == nil {
			t.Fatalf("parseChat(%v) returned nil error", args)
		}
	}
}

type fakeService struct {
	user       domain.User
	meeting    domain.MeetingView
	transcript string
	summary    string
	answer     string
	err        error
}

func (f *fakeService) StartUser(context.Context, string) (domain.User, error) { return f.user, f.err }
func (f *fakeService) LoadMeeting(context.Context, string, string) (domain.MeetingView, error) {
	return f.meeting, f.err
}
func (f *fakeService) ListMeetings(context.Context, string) ([]domain.MeetingView, error) {
	return []domain.MeetingView{f.meeting}, f.err
}
func (f *fakeService) Status(context.Context, string, int64) (domain.MeetingView, error) {
	return f.meeting, f.err
}
func (f *fakeService) Transcript(context.Context, string, int64) (string, error) {
	return f.transcript, f.err
}
func (f *fakeService) Summary(context.Context, string, int64) (string, error) {
	return f.summary, f.err
}
func (f *fakeService) Find(context.Context, string, string) ([]domain.SearchResult, error) {
	return nil, f.err
}
func (f *fakeService) Chat(context.Context, string, string, *int64) (string, error) {
	return f.answer, f.err
}
func (f *fakeService) Retry(context.Context, string, int64) error { return f.err }

var _ Service = (*fakeService)(nil)
