package app

import (
	"context"
	"errors"
	"testing"

	"meeting-notes/internal/domain"
)

func TestChatUsesCurrentUserContext(t *testing.T) {
	repo := &chatRepository{fakeRepository: fakeRepository{user: domain.User{ID: 17}}, contextParts: []string{"Алексей отвечает за тестирование."}}
	llm := &recordingLLM{answer: "Алексей."}
	service := New(repo, &fakeFiles{}, llm, nil)
	meetingID := int64(42)
	answer, err := service.Chat(context.Background(), "alice", "Кто отвечает?", &meetingID)
	if err != nil || answer != "Алексей." {
		t.Fatalf("Chat = %q, %v", answer, err)
	}
	if repo.contextUserID != 17 || repo.contextMeetingID == nil || *repo.contextMeetingID != 42 {
		t.Fatalf("context query = user %d, meeting %v", repo.contextUserID, repo.contextMeetingID)
	}
	if llm.context != "Алексей отвечает за тестирование." {
		t.Fatalf("LLM context = %q", llm.context)
	}
}

func TestChatRejectsEmptyQuestionAndRepositoryErrors(t *testing.T) {
	service := New(&fakeRepository{}, &fakeFiles{}, &recordingLLM{}, nil)
	if _, err := service.Chat(context.Background(), "alice", " ", nil); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("empty question error = %v", err)
	}
	repo := &chatRepository{fakeRepository: fakeRepository{user: domain.User{ID: 1}}, contextErr: domain.ErrNotFound}
	service = New(repo, &fakeFiles{}, &recordingLLM{}, nil)
	if _, err := service.Chat(context.Background(), "alice", "question", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("context error = %v", err)
	}
}

type chatRepository struct {
	fakeRepository
	contextParts     []string
	contextErr       error
	contextUserID    int64
	contextMeetingID *int64
}

func (r *chatRepository) ChatContext(_ context.Context, userID int64, meetingID *int64, _ int) ([]string, error) {
	r.contextUserID = userID
	r.contextMeetingID = meetingID
	return r.contextParts, r.contextErr
}

type recordingLLM struct {
	context string
	answer  string
	err     error
}

func (l *recordingLLM) Summarize(context.Context, string) (string, error) { return "", l.err }
func (l *recordingLLM) Answer(_ context.Context, contextText, _ string) (string, error) {
	l.context = contextText
	return l.answer, l.err
}
