package deploy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

const lockFileName = "agent.lock"

// RootLock holds an exclusive advisory lock for one managed output root.
type RootLock struct {
	file *os.File
}

// Close releases the lock. The lock file is intentionally retained to avoid
// unlink-and-replace races between competing processes.
func (l *RootLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// LockRoots acquires one non-blocking exclusive lock below every output root.
// Roots are processed in lexical order so multi-target processes cannot
// deadlock against each other.
func LockRoots(roots []string) ([]*RootLock, error) {
	ordered := append([]string(nil), roots...)
	sort.Strings(ordered)

	locks := make([]*RootLock, 0, len(ordered))
	for _, root := range ordered {
		lock, err := lockRoot(root)
		if err != nil {
			closeRootLocks(locks)
			return nil, fmt.Errorf("lock output root %q: %w", root, err)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func lockRoot(root string) (*RootLock, error) {
	managedRoot := filepath.Join(root, managedDirectory)
	if err := os.MkdirAll(managedRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create managed directory: %w", err)
	}

	file, err := os.OpenFile(filepath.Join(managedRoot, lockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("output root is already managed by another pemcast process")
		}
		return nil, fmt.Errorf("acquire flock: %w", err)
	}
	return &RootLock{file: file}, nil
}

func closeRootLocks(locks []*RootLock) {
	for _, lock := range locks {
		_ = lock.Close()
	}
}

// TargetRoots returns the output roots represented by targets.
func TargetRoots(targets []config.Target) []string {
	roots := make([]string, 0, len(targets))
	for _, target := range targets {
		roots = append(roots, target.Output.Root)
	}
	return roots
}
