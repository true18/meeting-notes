package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndRemove(t *testing.T) {
	source := filepath.Join(t.TempDir(), "meeting.txt")
	if err := os.WriteFile(source, []byte("meeting transcript"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(t.TempDir(), "uploads"), 1024)
	saved, err := store.Save(context.Background(), source)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Path == source || saved.Name != "meeting.txt" || saved.Format != "txt" {
		t.Fatalf("unexpected saved file: %+v", saved)
	}
	content, err := os.ReadFile(saved.Path)
	if err != nil || string(content) != "meeting transcript" {
		t.Fatalf("saved content = %q, err = %v", content, err)
	}
	if err := store.Remove(saved.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestSaveErrors(t *testing.T) {
	dir := t.TempDir()
	unsupported := filepath.Join(dir, "meeting.pdf")
	large := filepath.Join(dir, "meeting.txt")
	if err := os.WriteFile(unsupported, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(large, []byte("too large"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(dir, "uploads"), 2)
	if _, err := store.Save(context.Background(), filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := store.Save(context.Background(), dir); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := store.Save(context.Background(), unsupported); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("unsupported error = %v", err)
	}
	if _, err := store.Save(context.Background(), large); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("large error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	store = New(filepath.Join(dir, "uploads"), 1024)
	if _, err := store.Save(canceled, large); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}
