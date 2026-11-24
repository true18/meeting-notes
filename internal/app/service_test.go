package app

import (
	"context"
	"errors"
	"testing"

	"meeting-notes/internal/domain"
	"meeting-notes/internal/storage"
)

func TestStartUser(t *testing.T) {
	repo := &fakeRepository{user: domain.User{ID: 7, ExternalID: "alice"}}
	service := New(repo, &fakeFiles{}, nil, nil)
	user, err := service.StartUser(context.Background(), " alice ")
	if err != nil || user.ID != 7 || repo.lastExternalID != "alice" {
		t.Fatalf("StartUser = %+v, %v", user, err)
	}
	if _, err := service.StartUser(context.Background(), " "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty user error = %v", err)
	}
	repo.err = errors.New("database unavailable")
	if _, err := service.StartUser(context.Background(), "alice"); err == nil {
		t.Fatal("repository error not returned")
	}
}

func TestLoadMeetingCleansFileAfterTransactionError(t *testing.T) {
	repo := &fakeRepository{user: domain.User{ID: 1}, errCreate: errors.New("transaction failed")}
	files := &fakeFiles{saved: storage.SavedFile{Path: "/uploads/one.txt", Name: "one.txt", Format: "txt"}}
	service := New(repo, files, nil, nil)
	if _, err := service.LoadMeeting(context.Background(), "alice", "source.txt"); err == nil {
		t.Fatal("LoadMeeting returned nil error")
	}
	if files.removed != "/uploads/one.txt" {
		t.Fatalf("removed = %q", files.removed)
	}
}

func TestLoadMeetingSuccess(t *testing.T) {
	repo := &fakeRepository{user: domain.User{ID: 1}, created: domain.MeetingView{Meeting: domain.Meeting{ID: 11}, TaskID: 22, Status: domain.StatusCreated}}
	files := &fakeFiles{saved: storage.SavedFile{Path: "/uploads/one.txt", Name: "one.txt", Format: "txt"}}
	meeting, err := New(repo, files, nil, nil).LoadMeeting(context.Background(), "alice", "source.txt")
	if err != nil || meeting.ID != 11 || meeting.TaskID != 22 {
		t.Fatalf("LoadMeeting = %+v, %v", meeting, err)
	}
	if files.removed != "" {
		t.Fatalf("successful upload removed: %q", files.removed)
	}
}

func TestUserFacingQueries(t *testing.T) {
	repo := &fakeRepository{
		user:       domain.User{ID: 9, ExternalID: "alice"},
		meetings:   []domain.MeetingView{{Meeting: domain.Meeting{ID: 11}}},
		meeting:    domain.MeetingView{Meeting: domain.Meeting{ID: 11}},
		transcript: "transcript",
		summary:    "summary",
		results:    []domain.SearchResult{{MeetingID: 11}},
	}
	service := New(repo, &fakeFiles{}, nil, nil)
	ctx := context.Background()

	meetings, err := service.ListMeetings(ctx, "alice")
	if err != nil || len(meetings) != 1 || repo.lastUserID != 9 {
		t.Fatalf("ListMeetings = %+v, %v; user=%d", meetings, err, repo.lastUserID)
	}
	meeting, err := service.Status(ctx, "alice", 11)
	if err != nil || meeting.ID != 11 || repo.lastMeetingID != 11 {
		t.Fatalf("Status = %+v, %v", meeting, err)
	}
	transcript, err := service.Transcript(ctx, "alice", 11)
	if err != nil || transcript != "transcript" {
		t.Fatalf("Transcript = %q, %v", transcript, err)
	}
	summary, err := service.Summary(ctx, "alice", 11)
	if err != nil || summary != "summary" {
		t.Fatalf("Summary = %q, %v", summary, err)
	}
	results, err := service.Find(ctx, "alice", " релиз ")
	if err != nil || len(results) != 1 || repo.lastQuery != "релиз" || repo.lastLimit != searchLimit {
		t.Fatalf("Find = %+v, %v; query=%q limit=%d", results, err, repo.lastQuery, repo.lastLimit)
	}
	if err := service.Retry(ctx, "alice", 11); err != nil || !repo.retried {
		t.Fatalf("Retry = %v, retried=%t", err, repo.retried)
	}
	if _, err := service.Find(ctx, "alice", " "); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty Find error = %v", err)
	}
}

func TestUserFacingQueriesReturnErrors(t *testing.T) {
	service := New(&fakeRepository{err: errors.New("database unavailable")}, &fakeFiles{}, nil, nil)
	if _, err := service.ListMeetings(context.Background(), "alice"); err == nil {
		t.Fatal("ListMeetings returned nil error")
	}

	repo := &fakeRepository{user: domain.User{ID: 1}, errRead: domain.ErrNotFound}
	service = New(repo, &fakeFiles{}, nil, nil)
	if _, err := service.Status(context.Background(), "alice", 99); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Status error = %v", err)
	}
}

type fakeFiles struct {
	saved   storage.SavedFile
	err     error
	removed string
}

func (f *fakeFiles) Save(context.Context, string) (storage.SavedFile, error) { return f.saved, f.err }
func (f *fakeFiles) Remove(path string) error                                { f.removed = path; return nil }

type fakeRepository struct {
	user           domain.User
	err            error
	errRead        error
	errCreate      error
	created        domain.MeetingView
	meetings       []domain.MeetingView
	meeting        domain.MeetingView
	transcript     string
	summary        string
	results        []domain.SearchResult
	lastExternalID string
	lastUserID     int64
	lastMeetingID  int64
	lastQuery      string
	lastLimit      int
	retried        bool
}

func (f *fakeRepository) EnsureUser(_ context.Context, externalID string) (domain.User, error) {
	f.lastExternalID = externalID
	return f.user, f.err
}
func (f *fakeRepository) CreateMeetingWithTask(context.Context, int64, string, string, string) (domain.MeetingView, error) {
	return f.created, f.errCreate
}
func (f *fakeRepository) ListMeetings(_ context.Context, userID int64) ([]domain.MeetingView, error) {
	f.lastUserID = userID
	return f.meetings, errors.Join(f.err, f.errRead)
}
func (f *fakeRepository) GetMeeting(_ context.Context, userID, meetingID int64) (domain.MeetingView, error) {
	f.lastUserID, f.lastMeetingID = userID, meetingID
	return f.meeting, errors.Join(f.err, f.errRead)
}
func (f *fakeRepository) GetTranscript(_ context.Context, userID, meetingID int64) (string, error) {
	f.lastUserID, f.lastMeetingID = userID, meetingID
	return f.transcript, errors.Join(f.err, f.errRead)
}
func (f *fakeRepository) GetSummary(_ context.Context, userID, meetingID int64) (string, error) {
	f.lastUserID, f.lastMeetingID = userID, meetingID
	return f.summary, errors.Join(f.err, f.errRead)
}
func (f *fakeRepository) Search(_ context.Context, userID int64, query string, limit int) ([]domain.SearchResult, error) {
	f.lastUserID, f.lastQuery, f.lastLimit = userID, query, limit
	return f.results, errors.Join(f.err, f.errRead)
}
func (f *fakeRepository) ChatContext(context.Context, int64, *int64, int) ([]string, error) {
	return nil, f.err
}
func (f *fakeRepository) RetryTask(_ context.Context, userID, meetingID int64) error {
	f.lastUserID, f.lastMeetingID, f.retried = userID, meetingID, true
	return errors.Join(f.err, f.errRead)
}
