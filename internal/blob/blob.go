// Package blob stores the images the app generates and receives. The store
// records names; a Store keeps the bytes. Two backends: the local
// filesystem (development, tests) and Cloudflare R2 (production).
package blob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned for a name that holds nothing.
var ErrNotFound = errors.New("blob not found")

// Store is what the app needs from wherever the bytes live.
type Store interface {
	Put(ctx context.Context, name string, data []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	Delete(ctx context.Context, name string) error
	// URL returns a short-lived direct URL for a browser to fetch the blob
	// from, or "" when the app has to stream it itself.
	URL(ctx context.Context, name string) (string, error)
}

// ContentType picks the MIME type from a blob name.
func ContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

// safe rejects names that could escape a directory or address a key that
// was not minted by the app.
func safe(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\") && name != "." && name != ".."
}

// FS keeps blobs as files in one directory.
type FS struct {
	Dir string
}

// NewFS creates the directory and returns the store.
func NewFS(dir string) (*FS, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &FS{Dir: dir}, nil
}

func (f *FS) path(name string) (string, error) {
	if !safe(name) {
		return "", ErrNotFound
	}
	return filepath.Join(f.Dir, name), nil
}

func (f *FS) Put(ctx context.Context, name string, data []byte) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func (f *FS) Get(ctx context.Context, name string) ([]byte, error) {
	p, err := f.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

func (f *FS) Delete(ctx context.Context, name string) error {
	p, err := f.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// URL is empty: files are streamed by the app.
func (f *FS) URL(ctx context.Context, name string) (string, error) { return "", nil }

// Exists reports whether a file is present (used by the migration).
func (f *FS) Exists(name string) bool {
	p, err := f.path(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
