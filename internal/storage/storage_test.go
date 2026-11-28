package storage

import (
	"context"
	"errors"
	"io"
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
	t.Cleanup(func() { _ = store.Close() })
	saved, err := store.Save(context.Background(), source)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Path == source || saved.Name != "meeting.txt" || saved.Format != "txt" {
		t.Fatalf("unexpected saved file: %+v", saved)
	}
	contentFile, err := store.Open(saved.Path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	content, err := io.ReadAll(contentFile)
	_ = contentFile.Close()
	if err != nil || string(content) != "meeting transcript" {
		t.Fatalf("saved content = %q, err = %v", content, err)
	}
	if err := store.Remove(saved.Path); err != nil {
		t.Fatalf("Remove: %v", err)
	}
}

func TestRootRejectsPathsAndSymlinksOutside(t *testing.T) {
	base := t.TempDir()
	rootDir := filepath.Join(base, "root")
	outside := filepath.Join(base, "secret.txt")
	if err := os.Mkdir(rootDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New(rootDir, 1024)
	t.Cleanup(func() { _ = store.Close() })
	for _, path := range []string{"../secret.txt", outside} {
		if _, err := store.Open(path); err == nil {
			t.Fatalf("Open(%q) succeeded", path)
		}
		if err := store.Remove(path); err == nil {
			t.Fatalf("Remove(%q) succeeded", path)
		}
	}
	if err := os.Symlink(outside, filepath.Join(rootDir, "file-link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("file-link.txt"); err == nil {
		t.Fatal("opened symlink outside root")
	}
	if err := store.Remove("file-link.txt"); err != nil {
		t.Fatalf("remove link itself: %v", err)
	}
	externalDir := filepath.Join(base, "external")
	if err := os.Mkdir(externalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(externalDir, "inside.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalDir, filepath.Join(rootDir, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("dir-link/inside.txt"); err == nil {
		t.Fatal("opened file through symlink directory")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "do not touch" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(externalDir, "inside.txt")); err != nil || string(got) != "outside" {
		t.Fatalf("external file changed: %q, %v", got, err)
	}
}

func TestLegacyPathAndClosedStore(t *testing.T) {
	dir := t.TempDir()
	store := New(dir, 100)
	if err := os.WriteFile(filepath.Join(dir, "old.txt"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := store.Open(filepath.Join(dir, "old.txt"))
	if err != nil {
		t.Fatalf("open legacy path: %v", err)
	}
	got, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || string(got) != "legacy" {
		t.Fatalf("legacy read = %q, %v", got, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open("old.txt"); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("open after close = %v", err)
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
	t.Cleanup(func() { _ = store.Close() })
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
	entries, err := os.ReadDir(filepath.Join(dir, "uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("temporary upload was not cleaned: entries=%v err=%v", entries, err)
	}
}
