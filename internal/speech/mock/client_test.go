package mock

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/true18/meeting-notes/internal/speech"
)

func TestTranscribePreparedText(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "meeting.txt", "Обсудили релиз и сроки.")
	client, err := NewWithRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	text, err := client.Transcribe(context.Background(), speech.Request{Path: "meeting.txt", Name: "meeting.txt", Format: "txt"})
	if err != nil || text != "Обсудили релиз и сроки." {
		t.Fatalf("Transcribe = %q, %v", text, err)
	}
}

func TestTranscribeRejectsPathsOutsideRootAndSupportsLegacyPath(t *testing.T) {
	dir := t.TempDir()
	stored := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(stored, []byte("legacy transcript"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewWithRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	got, err := client.Transcribe(context.Background(), speech.Request{Path: stored, Format: "txt"})
	if err != nil || got != "legacy transcript" {
		t.Fatalf("legacy absolute path = %q, %v", got, err)
	}
	for _, path := range []string{"../outside.txt", outside} {
		if _, err := client.Transcribe(context.Background(), speech.Request{Path: path, Format: "txt"}); err == nil {
			t.Fatalf("transcribed external path %q", path)
		}
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "private" {
		t.Fatalf("external file changed: %q, %v", got, err)
	}
}

func TestTranscribeScenarios(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "error.txt", "[speech:error]")
	writeFixture(t, dir, "timeout.txt", "[speech:timeout]")
	client, err := NewWithRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Transcribe(context.Background(), speech.Request{Path: "error.txt", Format: "txt"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error scenario = %v", err)
	}
	if _, err := client.Transcribe(context.Background(), speech.Request{Name: "unknown.wav", Format: "wav"}); !errors.Is(err, ErrNoFixture) {
		t.Fatalf("audio scenario = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := client.Transcribe(ctx, speech.Request{Path: "timeout.txt", Format: "txt"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout scenario = %v", err)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := client.Transcribe(canceled, speech.Request{Path: "error.txt", Format: "txt"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled scenario = %v", err)
	}
}

func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
