// Package storage keeps durable copies of uploaded meeting files.
package storage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	// ErrUnsupportedFormat is returned for files that the application cannot process.
	ErrUnsupportedFormat = errors.New("unsupported meeting file format")
	// ErrFileTooLarge is returned when an upload exceeds the configured limit.
	ErrFileTooLarge = errors.New("meeting file is too large")
)

var supportedFormats = map[string]bool{
	".txt": true,
	".wav": true,
	".mp3": true,
	".m4a": true,
}

// SavedFile describes the application-owned copy of an upload.
type SavedFile struct {
	Path   string
	Name   string
	Format string
}

// Store copies uploads into an application-owned directory.
type Store struct {
	dir     string
	maxSize int64
	mu      sync.Mutex
	root    *os.Root
	closed  bool
}

// New creates a file store.
func New(dir string, maxSize int64) *Store {
	return &Store{dir: dir, maxSize: maxSize}
}

// Save validates and atomically copies source into the storage directory.
func (s *Store) Save(ctx context.Context, source string) (SavedFile, error) {
	info, err := os.Stat(source)
	if err != nil {
		return SavedFile{}, fmt.Errorf("inspect upload: %w", err)
	}
	if !info.Mode().IsRegular() {
		return SavedFile{}, fmt.Errorf("upload is not a regular file")
	}
	if info.Size() > s.maxSize {
		return SavedFile{}, ErrFileTooLarge
	}
	ext := strings.ToLower(filepath.Ext(info.Name()))
	if !supportedFormats[ext] {
		return SavedFile{}, fmt.Errorf("%w: %s", ErrUnsupportedFormat, ext)
	}
	root, err := s.openRoot()
	if err != nil {
		return SavedFile{}, fmt.Errorf("create upload directory: %w", err)
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		return SavedFile{}, fmt.Errorf("open upload: %w", err)
	}
	defer sourceFile.Close()

	tempName, err := randomName(".tmp")
	if err != nil {
		return SavedFile{}, err
	}
	temp, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return SavedFile{}, fmt.Errorf("create temporary upload: %w", err)
	}
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = root.Remove(tempName)
		}
	}()

	limited := &io.LimitedReader{R: sourceFile, N: s.maxSize + 1}
	if err := copyContext(ctx, temp, limited); err != nil {
		return SavedFile{}, fmt.Errorf("copy upload: %w", err)
	}
	if limited.N == 0 {
		return SavedFile{}, ErrFileTooLarge
	}
	if err := temp.Sync(); err != nil {
		return SavedFile{}, fmt.Errorf("sync upload: %w", err)
	}
	if err := temp.Close(); err != nil {
		return SavedFile{}, fmt.Errorf("close upload: %w", err)
	}
	name, err := randomName(ext)
	if err != nil {
		return SavedFile{}, err
	}
	if err := root.Rename(tempName, name); err != nil {
		return SavedFile{}, fmt.Errorf("commit upload: %w", err)
	}
	keep = true
	return SavedFile{Path: name, Name: info.Name(), Format: strings.TrimPrefix(ext, ".")}, nil
}

// Remove deletes an application-owned upload.
func (s *Store) Remove(path string) error {
	name, err := s.relativeName(path)
	if err != nil {
		return err
	}
	root, err := s.openRoot()
	if err != nil {
		return err
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upload: %w", err)
	}
	return nil
}

// Open opens a stored file, accepting legacy absolute paths only inside the store.
func (s *Store) Open(path string) (*os.File, error) {
	name, err := s.relativeName(path)
	if err != nil {
		return nil, err
	}
	root, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("open stored upload: %w", err)
	}
	return file, nil
}

// Close releases the directory handle. Call it after workers stop using the store.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.root == nil {
		return nil
	}
	err := s.root.Close()
	s.root = nil
	return err
}

func (s *Store) openRoot() (*os.Root, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.root != nil {
		return s.root, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(s.dir)
	if err != nil {
		return nil, err
	}
	s.root = root
	return root, nil
}

func (s *Store) relativeName(path string) (string, error) {
	if filepath.IsAbs(path) {
		root, err := filepath.Abs(s.dir)
		if err != nil {
			return "", fmt.Errorf("resolve storage directory: %w", err)
		}
		name, err := filepath.Rel(root, path)
		if err != nil || name == "." || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("stored path is outside storage root")
		}
		path = name
	}
	name := filepath.Clean(path)
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid storage path")
	}
	return name, nil
}

func randomName(ext string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate upload name: %w", err)
	}
	return hex.EncodeToString(buffer) + ext, nil
}

func copyContext(ctx context.Context, dst io.Writer, src io.Reader) error {
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if _, err := dst.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
