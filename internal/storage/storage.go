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
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return SavedFile{}, fmt.Errorf("create upload directory: %w", err)
	}

	sourceFile, err := os.Open(source)
	if err != nil {
		return SavedFile{}, fmt.Errorf("open upload: %w", err)
	}
	defer sourceFile.Close()

	temp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return SavedFile{}, fmt.Errorf("create temporary upload: %w", err)
	}
	tempName := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempName)
		}
	}()

	if err := copyContext(ctx, temp, sourceFile); err != nil {
		return SavedFile{}, fmt.Errorf("copy upload: %w", err)
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
	destination := filepath.Join(s.dir, name)
	if err := os.Rename(tempName, destination); err != nil {
		return SavedFile{}, fmt.Errorf("commit upload: %w", err)
	}
	keep = true
	return SavedFile{Path: destination, Name: info.Name(), Format: strings.TrimPrefix(ext, ".")}, nil
}

// Remove deletes an application-owned upload.
func (s *Store) Remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove upload: %w", err)
	}
	return nil
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
