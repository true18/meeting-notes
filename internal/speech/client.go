// Package speech defines the speech-to-text boundary.
package speech

import "context"

// Request describes a durable meeting file passed to a speech provider.
type Request struct {
	Path   string
	Name   string
	Format string
}

// Client transcribes a meeting file.
type Client interface {
	Transcribe(context.Context, Request) (string, error)
}
