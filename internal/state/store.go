// Package state persists the small amount of local reconciliation state.
package state

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Target is the last activated state for one configured target.
type Target struct {
	Generation  string    `json:"generation"`
	Digest      string    `json:"digest"`
	Revision    int64     `json:"revision"`
	ActivatedAt time.Time `json:"activated-at"`
	HookError   string    `json:"hook-error"`
}

// Store owns state files below one private directory.
type Store struct{ dir string }

func New(dir string) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("state directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) Load(targetID string) (Target, error) {
	data, err := os.ReadFile(s.path(targetID))
	if errors.Is(err, os.ErrNotExist) {
		return Target{}, nil
	}
	if err != nil {
		return Target{}, fmt.Errorf("read target state: %w", err)
	}
	var target Target
	if err := json.Unmarshal(data, &target, json.RejectUnknownMembers(true)); err != nil {
		return Target{}, fmt.Errorf("decode target state: %w", err)
	}
	return target, nil
}

func (s *Store) Save(targetID string, target Target) error {
	data, err := json.Marshal(target)
	if err != nil {
		return fmt.Errorf("encode target state: %w", err)
	}
	return writeAtomic(s.path(targetID), append(data, '\n'), 0o600)
}

func (s *Store) path(targetID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(targetID)
	return filepath.Join(s.dir, safe+".json")
}

func writeAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".pemcast-state-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
