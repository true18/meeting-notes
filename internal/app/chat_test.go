package app

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/true18/meeting-notes/internal/domain"
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

func TestBoundedContext(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		limit int
		want  string
	}{
		{name: "empty", limit: 4},
		{name: "short", parts: []string{"hello"}, limit: 10, want: "hello"},
		{name: "exact boundary", parts: []string{strings.Repeat("a", 12000)}, limit: 12000, want: strings.Repeat("a", 12000)},
		{name: "over boundary", parts: []string{strings.Repeat("a", 12001)}, limit: 12000, want: strings.Repeat("a", 12000)},
		{name: "russian boundary", parts: []string{"a" + strings.Repeat("я", 12001)}, limit: 12000, want: "a" + strings.Repeat("я", 11999)},
		{name: "emoji boundary", parts: []string{"a🙂b"}, limit: 2, want: "a🙂"},
		{name: "mixed languages", parts: []string{"Go: встреча"}, limit: 7, want: "Go: вст"},
		{name: "multiple meetings", parts: []string{"аб", "cd", "🙂я"}, limit: 8, want: "аб\ncd\n🙂я"},
		{name: "separator counts", parts: []string{"аб", "cd"}, limit: 3, want: "аб\n"},
		{name: "zero limit", parts: []string{"text"}, limit: 0},
		{name: "negative limit", parts: []string{"text"}, limit: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := boundedContext(sequence(tt.parts), tt.limit)
			if got != tt.want {
				t.Fatalf("boundedContext() = %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) || utf8.RuneCountInString(got) > max(tt.limit, 0) {
				t.Fatalf("result is invalid UTF-8 or exceeds limit: %q", got)
			}
		})
	}
}

func TestBoundedContextStopsSequenceWhenLimitReached(t *testing.T) {
	visited := 0
	parts := iter.Seq[string](func(yield func(string) bool) {
		for _, part := range []string{"ab", "cd", "must not visit"} {
			visited++
			if !yield(part) {
				return
			}
		}
	})
	got := boundedContext(parts, 5)
	if got != "ab\ncd" {
		t.Fatalf("boundedContext() = %q", got)
	}
	if visited != 2 {
		t.Fatalf("visited %d elements after limit, want 2", visited)
	}
}

func sequence(parts []string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, part := range parts {
			if !yield(part) {
				return
			}
		}
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
