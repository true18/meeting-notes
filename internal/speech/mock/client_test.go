package mock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"meeting-notes/internal/speech"
)

func TestTranscribePreparedText(t *testing.T) {
	path := writeFixture(t, "meeting.txt", "Обсудили релиз и сроки.")
	text, err := New().Transcribe(context.Background(), speech.Request{Path: path, Name: "meeting.txt", Format: "txt"})
	if err != nil || text != "Обсудили релиз и сроки." {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
}

func TestTranscribeScenarios(t *testing.T) {
	client := New()
	errorPath := writeFixture(t, "error.txt", "[speech:error]")
	if _, err := client.Transcribe(context.Background(), speech.Request{Path: errorPath, Format: "txt"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error scenario = %v", err)
	}
	if _, err := client.Transcribe(context.Background(), speech.Request{Name: "unknown.wav", Format: "wav"}); !errors.Is(err, ErrNoFixture) {
		t.Fatalf("audio scenario = %v", err)
	}
	timeoutPath := writeFixture(t, "timeout.txt", "[speech:timeout]")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Transcribe(ctx, speech.Request{Path: timeoutPath, Format: "txt"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout scenario = %v", err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := client.Transcribe(canceled, speech.Request{Path: errorPath, Format: "txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scenario = %v", err)
	}
}

func writeFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
