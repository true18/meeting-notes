// Package llm defines text summarization and question-answering boundaries.
package llm

import "context"

// Client summarizes transcripts and answers questions from supplied context.
type Client interface {
	Summarize(context.Context, string) (string, error)
	Answer(context.Context, string, string) (string, error)
}
