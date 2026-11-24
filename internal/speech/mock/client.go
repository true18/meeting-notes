// Package mock provides a deterministic speech client for demonstrations and tests.
package mock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"meeting-notes/internal/speech"
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
type Client struct{}

// New creates a mock speech client.
func New() *Client { return &Client{} }

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
	data, err := os.ReadFile(request.Path)
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
