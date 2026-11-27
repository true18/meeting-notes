// Package app implements user-facing meeting use cases.
package app

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/true18/meeting-notes/internal/domain"
	"github.com/true18/meeting-notes/internal/llm"
	"github.com/true18/meeting-notes/internal/storage"
)

const (
	searchLimit      = 20
	chatMeetingLimit = 5
)

// Repository is the persistence boundary used by user-facing use cases.
type Repository interface {
	EnsureUser(context.Context, string) (domain.User, error)
	CreateMeetingWithTask(context.Context, int64, string, string, string) (domain.MeetingView, error)
	ListMeetings(context.Context, int64) ([]domain.MeetingView, error)
	GetMeeting(context.Context, int64, int64) (domain.MeetingView, error)
	GetTranscript(context.Context, int64, int64) (string, error)
	GetSummary(context.Context, int64, int64) (string, error)
	Search(context.Context, int64, string, int) ([]domain.SearchResult, error)
	ChatContext(context.Context, int64, *int64, int) ([]string, error)
	RetryTask(context.Context, int64, int64) error
}

// FileStore saves and removes application-owned uploads.
type FileStore interface {
	Save(context.Context, string) (storage.SavedFile, error)
	Remove(string) error
}

// Service coordinates user-facing operations.
type Service struct {
	repo   Repository
	files  FileStore
	llm    llm.Client
	logger *slog.Logger
}

// New creates an application service.
func New(repo Repository, files FileStore, llmClient llm.Client, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{repo: repo, files: files, llm: llmClient, logger: logger}
}

// StartUser creates or returns a user.
func (s *Service) StartUser(ctx context.Context, externalID string) (domain.User, error) {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return domain.User{}, fmt.Errorf("%w: user ID is empty", domain.ErrInvalidInput)
	}
	user, err := s.repo.EnsureUser(ctx, externalID)
	if err != nil {
		return domain.User{}, fmt.Errorf("start user: %w", err)
	}
	s.logger.InfoContext(ctx, "user ready", "user_id", user.ID)
	return user, nil
}

// LoadMeeting saves a durable file copy and atomically creates a meeting and task.
func (s *Service) LoadMeeting(ctx context.Context, externalID, source string) (domain.MeetingView, error) {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return domain.MeetingView{}, err
	}
	saved, err := s.files.Save(ctx, source)
	if err != nil {
		return domain.MeetingView{}, fmt.Errorf("save meeting file: %w", err)
	}
	meeting, err := s.repo.CreateMeetingWithTask(ctx, user.ID, saved.Name, saved.Path, saved.Format)
	if err != nil {
		cleanupErr := s.files.Remove(saved.Path)
		return domain.MeetingView{}, errors.Join(fmt.Errorf("create meeting: %w", err), cleanupErr)
	}
	s.logger.InfoContext(ctx, "meeting queued", "user_id", user.ID, "meeting_id", meeting.ID, "task_id", meeting.TaskID)
	return meeting, nil
}

// ListMeetings returns meetings owned by externalID.
func (s *Service) ListMeetings(ctx context.Context, externalID string) ([]domain.MeetingView, error) {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListMeetings(ctx, user.ID)
}

// Status returns one user-owned meeting.
func (s *Service) Status(ctx context.Context, externalID string, meetingID int64) (domain.MeetingView, error) {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return domain.MeetingView{}, err
	}
	return s.repo.GetMeeting(ctx, user.ID, meetingID)
}

// Transcript returns one user-owned transcript.
func (s *Service) Transcript(ctx context.Context, externalID string, meetingID int64) (string, error) {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return "", err
	}
	return s.repo.GetTranscript(ctx, user.ID, meetingID)
}

// Summary returns one user-owned summary.
func (s *Service) Summary(ctx context.Context, externalID string, meetingID int64) (string, error) {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return "", err
	}
	return s.repo.GetSummary(ctx, user.ID, meetingID)
}

// Find searches user-owned meeting materials.
func (s *Service) Find(ctx context.Context, externalID, query string) ([]domain.SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: search query is empty", domain.ErrInvalidInput)
	}
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return nil, err
	}
	return s.repo.Search(ctx, user.ID, query, searchLimit)
}

// Chat answers a question using only bounded context owned by externalID.
func (s *Service) Chat(ctx context.Context, externalID, question string, meetingID *int64) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("%w: question is empty", domain.ErrInvalidInput)
	}
	if s.llm == nil {
		return "", errors.New("LLM client is not configured")
	}
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return "", err
	}
	parts, err := s.repo.ChatContext(ctx, user.ID, meetingID, chatMeetingLimit)
	if err != nil {
		return "", err
	}
	contextText := boundedContext(slices.Values(parts), 12000)
	answer, err := s.llm.Answer(ctx, contextText, question)
	if err != nil {
		return "", fmt.Errorf("answer question: %w", err)
	}
	return answer, nil
}

func boundedContext(parts iter.Seq[string], limit int) string {
	if limit <= 0 {
		return ""
	}
	var result strings.Builder
	remaining := limit
	first := true
	parts(func(part string) bool {
		if !first {
			if remaining == 0 {
				return false
			}
			result.WriteByte('\n')
			remaining--
		}
		first = false
		if utf8.RuneCountInString(part) > remaining {
			for _, r := range part {
				if remaining == 0 {
					return false
				}
				result.WriteRune(r)
				remaining--
			}
			return false
		}
		result.WriteString(part)
		remaining -= utf8.RuneCountInString(part)
		return remaining > 0
	})
	return result.String()
}

// Retry requeues a failed user-owned meeting.
func (s *Service) Retry(ctx context.Context, externalID string, meetingID int64) error {
	user, err := s.StartUser(ctx, externalID)
	if err != nil {
		return err
	}
	return s.repo.RetryTask(ctx, user.ID, meetingID)
}
