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
	errCreate      error
	created        domain.MeetingView
	lastExternalID string
}

func (f *fakeRepository) EnsureUser(_ context.Context, externalID string) (domain.User, error) {
	f.lastExternalID = externalID
	return f.user, f.err
}
func (f *fakeRepository) CreateMeetingWithTask(context.Context, int64, string, string, string) (domain.MeetingView, error) {
	return f.created, f.errCreate
}
func (f *fakeRepository) ListMeetings(context.Context, int64) ([]domain.MeetingView, error) {
	return nil, f.err
}
func (f *fakeRepository) GetMeeting(context.Context, int64, int64) (domain.MeetingView, error) {
	return domain.MeetingView{}, f.err
}
func (f *fakeRepository) GetTranscript(context.Context, int64, int64) (string, error) {
	return "", f.err
}
func (f *fakeRepository) GetSummary(context.Context, int64, int64) (string, error) { return "", f.err }
func (f *fakeRepository) Search(context.Context, int64, string, int) ([]domain.SearchResult, error) {
	return nil, f.err
}
func (f *fakeRepository) ChatContext(context.Context, int64, *int64, int) ([]string, error) {
	return nil, f.err
}
func (f *fakeRepository) RetryTask(context.Context, int64, int64) error { return f.err }
