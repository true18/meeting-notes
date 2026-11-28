// Package mock provides a deterministic speech client for demonstrations and tests.
package mock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/true18/meeting-notes/internal/speech"
)

var (
	// ErrUnavailable simulates a speech provider failure.
	ErrUnavailable = errors.New("mock speech service unavailable")
	// ErrNoFixture indicates that mock mode cannot recognize arbitrary audio.
	ErrNoFixture = errors.New("mock transcript is not configured for this audio file")
)

var audioFixtures = map[string]string{
	"meeting_release.wav": "Обсудили релиз. Решили завершить тестирование до пятницы. Алексей отвечает за тестирование. Мария подготовит документацию.",
}

// Client implements deterministic test transcription.
type Client struct {
	root *os.Root
	dir  string
}

// New creates a mock speech client.
func New() *Client { return &Client{} }

// NewWithRoot creates a client that reads prepared transcripts only through a directory root.
func NewWithRoot(dir string) (*Client, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create speech storage directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open speech storage root: %w", err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("resolve speech storage directory: %w", err)
	}
	return &Client{root: root, dir: absDir}, nil
}

// Close releases the directory root opened by NewWithRoot.
func (c *Client) Close() error {
	if c.root == nil {
		return nil
	}
	return c.root.Close()
}

// Transcribe reads prepared text fixtures or returns configured audio fixture text.
func (c *Client) Transcribe(ctx context.Context, request speech.Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if request.Format != "txt" {
		transcript, ok := audioFixtures[request.Name]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrNoFixture, request.Name)
		}
		return transcript, nil
	}
	if c.root == nil {
		return "", errors.New("mock speech storage root is not configured")
	}
	name := request.Path
	if filepath.IsAbs(name) {
		var err error
		name, err = filepath.Rel(c.dir, name)
		if err != nil || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", errors.New("mock transcript path is outside storage root")
		}
	}
	file, err := c.root.Open(name)
	if err != nil {
		return "", fmt.Errorf("read mock transcript: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		return "", fmt.Errorf("read mock transcript: %w", err)
	}
	text := strings.TrimSpace(string(data))
	switch {
	case strings.HasPrefix(text, "[speech:error]"):
		return "", ErrUnavailable
	case strings.HasPrefix(text, "[speech:timeout]"):
		<-ctx.Done()
		return "", ctx.Err()
	case text == "":
		return "", errors.New("mock transcript is empty")
	default:
		return text, nil
	}
}
