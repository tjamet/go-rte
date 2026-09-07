package tempo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// FileStore caches days on the local filesystem, one file per day under
// Root/YYYY/MM/DD.json.
type FileStore struct {
	// Root is the directory the tree lives in. It is created as needed.
	Root string
}

// Get returns the file at key, or ErrNotCached if it does not exist.
func (s *FileStore) Get(_ context.Context, key string) ([]byte, error) {
	name, err := s.resolve(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("tempo: %s: %w", key, ErrNotCached)
	}
	if err != nil {
		return nil, fmt.Errorf("tempo: read %s: %w", key, err)
	}
	return data, nil
}

// Put writes data at key.
//
// It writes to a temporary file in the destination directory and renames it
// into place. A process killed mid-write then leaves either the old file or the
// new one, never a truncated file that would decode as a valid entry with the
// wrong colour.
func (s *FileStore) Put(_ context.Context, key string, data []byte) error {
	name, err := s.resolve(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tempo: create %s: %w", dir, err)
	}

	temp, err := os.CreateTemp(dir, "."+filepath.Base(name)+".*")
	if err != nil {
		return fmt.Errorf("tempo: create temporary file in %s: %w", dir, err)
	}
	defer func() {
		// Best-effort cleanup: a no-op once the rename below succeeds, and
		// not worth failing Put over if an early return left it behind.
		_ = os.Remove(temp.Name())
	}()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close() // the write error above is what we report
		return fmt.Errorf("tempo: write %s: %w", key, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("tempo: close %s: %w", key, err)
	}
	if err := os.Rename(temp.Name(), name); err != nil {
		return fmt.Errorf("tempo: rename into %s: %w", key, err)
	}
	return nil
}

// List returns the keys under prefix. A directory that was never written is
// empty rather than an error: on a cold cache most months are missing, and the
// planner lists every month its window spans.
func (s *FileStore) List(_ context.Context, prefix string) ([]string, error) {
	dir, err := s.resolve(prefix)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("tempo: list %s: %w", prefix, err)
	}

	keys := make([]string, 0, len(items))
	for _, item := range items {
		if item.IsDir() || strings.HasPrefix(item.Name(), ".") {
			continue
		}
		keys = append(keys, path.Join(prefix, item.Name()))
	}
	return keys, nil
}

// resolve turns a store key into a path under Root.
//
// A key climbing out of the root is rejected rather than cleaned back in.
// Every key this package produces comes from [Date.Key] or [FloorKey], so one
// that escapes means a caller built its own and got it wrong; silently
// rewriting it would hide that and write the day to the wrong file.
func (s *FileStore) resolve(key string) (string, error) {
	if slices.Contains(strings.Split(key, "/"), "..") {
		return "", fmt.Errorf("tempo: key %q escapes the store root", key)
	}
	return filepath.Join(s.Root, filepath.FromSlash(path.Clean("/"+key))), nil
}
