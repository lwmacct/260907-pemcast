package deploy

import (
	"errors"
	"fmt"
	"io/fs"
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

// LockRoots acquires one non-blocking exclusive lock below every target root.
// Roots are processed in lexical order so multi-target processes cannot
// deadlock against each other.
func LockRoots(targets []config.Target) ([]*RootLock, error) {
	ordered := append([]config.Target(nil), targets...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Output.Root < ordered[j].Output.Root
	})

	locks := make([]*RootLock, 0, len(ordered))
	for _, target := range ordered {
		lock, err := lockRoot(target.Output.Root, target.Output.DirectoryMode.Perm())
		if err != nil {
			closeRootLocks(locks)
			return nil, fmt.Errorf("lock output root %q: %w", target.Output.Root, err)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func lockRoot(root string, directoryMode fs.FileMode) (*RootLock, error) {
	if err := ensureManagedRoot(root, directoryMode); err != nil {
		return nil, err
	}

	managedRoot := filepath.Join(root, managedDirectory)
	if err := ensureDirectory(managedRoot, directoryMode); err != nil {
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

func ensureManagedRoot(root string, directoryMode fs.FileMode) error {
	if err := ensureDirectory(root, directoryMode); err != nil {
		return fmt.Errorf("create output root: %w", err)
	}
	return nil
}

func ensureDirectory(path string, mode fs.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("path %q is not a directory", path)
	}
	if info.Mode().Perm() != mode {
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	return nil
}

func closeRootLocks(locks []*RootLock) {
	for _, lock := range locks {
		_ = lock.Close()
	}
}
