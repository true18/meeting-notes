// Package domain contains the core meeting-processing entities.
package domain

import (
	"errors"
	"time"
)

var (
	// ErrNotFound is returned for a missing meeting or one owned by another user.
	ErrNotFound = errors.New("meeting not found")
	// ErrNotReady is returned when a meeting has no completed transcript yet.
	ErrNotReady = errors.New("meeting is not processed yet")
	// ErrInvalidInput is returned for invalid user input.
	ErrInvalidInput = errors.New("invalid input")
)

// TaskStatus describes a processing task lifecycle state.
type TaskStatus string

const (
	StatusCreated     TaskStatus = "created"
	StatusProcessing  TaskStatus = "processing"
	StatusTranscribed TaskStatus = "transcribed"
	StatusSummarized  TaskStatus = "summarized"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
)

// User identifies a CLI user.
type User struct {
	ID         int64
	ExternalID string
	CreatedAt  time.Time
}

// Meeting stores file metadata and ownership.
type Meeting struct {
	ID        int64
	UserID    int64
	Title     string
	FilePath  string
	Format    string
	CreatedAt time.Time
}

// ProcessingTask tracks asynchronous meeting processing.
type ProcessingTask struct {
	ID           int64
	MeetingID    int64
	Status       TaskStatus
	ErrorMessage string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	StartedAt    *time.Time
	FinishedAt   *time.Time
}

// MeetingView is a user-facing meeting with its current processing state.
type MeetingView struct {
	Meeting
	TaskID       int64
	Status       TaskStatus
	ErrorMessage string
	UpdatedAt    time.Time
	Summary      string
}

// ClaimedTask contains everything a worker needs after atomically claiming a task.
type ClaimedTask struct {
	TaskID      int64
	MeetingID   int64
	UserID      int64
	FilePath    string
	FileFormat  string
	MeetingName string
}

// SearchResult is a meeting snippet returned by text search.
type SearchResult struct {
	MeetingID int64
	Title     string
	Status    TaskStatus
	CreatedAt time.Time
	Snippet   string
}
