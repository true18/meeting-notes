package domain

import "testing"

func TestTaskStatusesAreDistinct(t *testing.T) {
	statuses := []TaskStatus{
		StatusCreated,
		StatusProcessing,
		StatusTranscribed,
		StatusSummarized,
		StatusCompleted,
		StatusFailed,
	}
	seen := make(map[TaskStatus]struct{}, len(statuses))
	for _, status := range statuses {
		if status == "" {
			t.Fatal("empty status")
		}
		if _, ok := seen[status]; ok {
			t.Fatalf("duplicate status %q", status)
		}
		seen[status] = struct{}{}
	}
}
