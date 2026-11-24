package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/true18/meeting-notes/internal/domain"
)

// Service contains user-facing application operations used by the CLI.
type Service interface {
	StartUser(context.Context, string) (domain.User, error)
	LoadMeeting(context.Context, string, string) (domain.MeetingView, error)
	ListMeetings(context.Context, string) ([]domain.MeetingView, error)
	Status(context.Context, string, int64) (domain.MeetingView, error)
	Transcript(context.Context, string, int64) (string, error)
	Summary(context.Context, string, int64) (string, error)
	Find(context.Context, string, string) ([]domain.SearchResult, error)
	Chat(context.Context, string, string, *int64) (string, error)
	Retry(context.Context, string, int64) error
}

// Handler executes parsed commands.
type Handler struct {
	Service   Service
	Migrate   func(context.Context) error
	RunWorker func(context.Context) error
}

// Execute runs command and writes stable human-readable output.
func (h Handler) Execute(ctx context.Context, command Command, stdout, stderr io.Writer) int {
	if err := h.execute(ctx, command, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", userError(err))
		return 1
	}
	return 0
}

func (h Handler) execute(ctx context.Context, command Command, out io.Writer) error {
	switch command.Name {
	case "migrate":
		if len(command.Arguments) != 0 {
			return errors.New("migrate does not accept arguments")
		}
		if err := h.Migrate(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		fmt.Fprintln(out, "migrations applied")
	case "worker":
		if len(command.Arguments) != 0 {
			return errors.New("worker does not accept arguments")
		}
		fmt.Fprintln(out, "worker started")
		if err := h.RunWorker(ctx); err != nil {
			return fmt.Errorf("run worker: %w", err)
		}
	case "start":
		if len(command.Arguments) != 0 {
			return errors.New("start does not accept arguments")
		}
		user, err := h.Service.StartUser(ctx, command.User)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "user_id=%d user=%s\n", user.ID, user.ExternalID)
	case "load":
		if len(command.Arguments) != 1 {
			return errors.New("load requires one file path")
		}
		meeting, err := h.Service.LoadMeeting(ctx, command.User, command.Arguments[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "meeting_id=%d task_id=%d status=%s\n", meeting.ID, meeting.TaskID, meeting.Status)
	case "list":
		if len(command.Arguments) != 0 {
			return errors.New("list does not accept arguments")
		}
		meetings, err := h.Service.ListMeetings(ctx, command.User)
		if err != nil {
			return err
		}
		if len(meetings) == 0 {
			fmt.Fprintln(out, "no meetings")
			return nil
		}
		for _, meeting := range meetings {
			fmt.Fprintf(out, "id=%d created=%s status=%s title=%q", meeting.ID, meeting.CreatedAt.Format("2006-01-02 15:04:05"), meeting.Status, meeting.Title)
			if meeting.Summary != "" {
				fmt.Fprintf(out, " summary=%q", meeting.Summary)
			}
			fmt.Fprintln(out)
		}
	case "status":
		meeting, err := h.Service.Status(ctx, command.User, command.MeetingID)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "meeting_id=%d task_id=%d status=%s created=%s updated=%s\n", meeting.ID, meeting.TaskID, meeting.Status, meeting.CreatedAt.Format("2006-01-02 15:04:05"), meeting.UpdatedAt.Format("2006-01-02 15:04:05"))
		if meeting.ErrorMessage != "" {
			fmt.Fprintf(out, "error=%s\n", meeting.ErrorMessage)
		}
	case "get":
		text, err := h.Service.Transcript(ctx, command.User, command.MeetingID)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, text)
	case "summary":
		text, err := h.Service.Summary(ctx, command.User, command.MeetingID)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, text)
	case "find":
		if len(command.Arguments) == 0 {
			return errors.New("find requires a search query")
		}
		results, err := h.Service.Find(ctx, command.User, strings.Join(command.Arguments, " "))
		if err != nil {
			return err
		}
		if len(results) == 0 {
			fmt.Fprintln(out, "no matches")
			return nil
		}
		for _, result := range results {
			fmt.Fprintf(out, "meeting_id=%d created=%s status=%s title=%q snippet=%q\n", result.MeetingID, result.CreatedAt.Format("2006-01-02 15:04:05"), result.Status, result.Title, result.Snippet)
		}
	case "chat":
		meetingID, question, err := parseChat(command.Arguments)
		if err != nil {
			return err
		}
		answer, err := h.Service.Chat(ctx, command.User, question, meetingID)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, answer)
	case "retry":
		if err := h.Service.Retry(ctx, command.User, command.MeetingID); err != nil {
			return err
		}
		fmt.Fprintf(out, "meeting_id=%d status=%s\n", command.MeetingID, domain.StatusCreated)
	default:
		return fmt.Errorf("unknown command %q", command.Name)
	}
	return nil
}

func parseChat(arguments []string) (*int64, string, error) {
	flags := flag.NewFlagSet("chat", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	meeting := flags.String("meeting", "", "limit context to one meeting")
	if err := flags.Parse(arguments); err != nil {
		return nil, "", err
	}
	question := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if question == "" {
		return nil, "", errors.New("chat requires a question")
	}
	if *meeting == "" {
		return nil, question, nil
	}
	id, err := strconv.ParseInt(*meeting, 10, 64)
	if err != nil || id < 1 {
		return nil, "", fmt.Errorf("invalid meeting ID %q", *meeting)
	}
	return &id, question, nil
}

func userError(err error) string {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "meeting not found"
	case errors.Is(err, domain.ErrNotReady):
		return "meeting processing is not complete"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, context.Canceled):
		return "operation canceled"
	default:
		return err.Error()
	}
}
