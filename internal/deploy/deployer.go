// Package deploy atomically activates verified bundles using release directories.
package deploy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
)

const managedDirectory = ".pemcast"

// Result describes an active local release.
type Result struct {
	Changed    bool
	ReleaseDir string
	CurrentDir string
}

// Deployer installs immutable local release directories and switches one symlink.
type Deployer struct{}

func New() *Deployer { return &Deployer{} }

// CurrentDigest reads the content identity stored in the selected release.
func (d *Deployer) CurrentDigest(output config.Output) (string, error) {
	data, err := os.ReadFile(filepath.Join(output.Root, output.CurrentLink, ".pemcast-digest"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// Activate writes a complete release and atomically replaces current-link.
func (d *Deployer) Activate(material *bundle.Material, output config.Output) (Result, error) {
	if material == nil {
		return Result{}, fmt.Errorf("bundle material is nil")
	}
	currentDigest, err := d.CurrentDigest(output)
	if err != nil {
		return Result{}, fmt.Errorf("read active release digest: %w", err)
	}
	currentDir := filepath.Join(output.Root, output.CurrentLink)
	if currentDigest == material.Digest {
		return Result{CurrentDir: currentDir}, nil
	}
	managedRoot := filepath.Join(output.Root, managedDirectory)
	releasesRoot := filepath.Join(managedRoot, "releases")
	if err := os.MkdirAll(releasesRoot, output.DirectoryMode.Perm()); err != nil {
		return Result{}, fmt.Errorf("create releases directory: %w", err)
	}
	releaseName := "sha256-" + material.Digest
	releaseDir := filepath.Join(releasesRoot, releaseName)
	if err := d.ensureRelease(releaseDir, material, output); err != nil {
		return Result{}, err
	}
	if err := switchSymlink(output.Root, output.CurrentLink, filepath.Join(managedDirectory, "releases", releaseName)); err != nil {
		return Result{}, fmt.Errorf("activate release: %w", err)
	}
	if err := pruneReleases(releasesRoot, releaseName, output.RetainReleases); err != nil {
		return Result{}, fmt.Errorf("prune old releases: %w", err)
	}
	return Result{Changed: true, ReleaseDir: releaseDir, CurrentDir: currentDir}, nil
}

func (d *Deployer) ensureRelease(releaseDir string, material *bundle.Material, output config.Output) error {
	if info, err := os.Stat(releaseDir); err == nil && info.IsDir() {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(releaseDir), ".staging-*")
	if err != nil {
		return fmt.Errorf("create release staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if err := os.Chmod(temporary, output.DirectoryMode.Perm()); err != nil {
		return err
	}
	for _, mapping := range output.Mappings {
		content, ok := material.Files[mapping.Remote]
		if !ok {
			return fmt.Errorf("mapped bundle file %q is missing", mapping.Remote)
		}
		destination := filepath.Join(temporary, mapping.Local)
		if err := writeReleaseFile(destination, content, mapping.Mode.Perm(), output.DirectoryMode.Perm()); err != nil {
			return fmt.Errorf("write release file %q: %w", mapping.Local, err)
		}
	}
	if err := writeReleaseFile(filepath.Join(temporary, ".pemcast-digest"), []byte(material.Digest+"\n"), 0o600, output.DirectoryMode.Perm()); err != nil {
		return err
	}
	if err := syncDirectoryTree(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, releaseDir); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return fmt.Errorf("commit release directory: %w", err)
	}
	return syncDirectory(filepath.Dir(releaseDir))
}

func writeReleaseFile(path string, content []byte, mode, directoryMode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
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
	return file.Close()
}

func switchSymlink(root, name, target string) error {
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o755); err != nil {
		return err
	}
	temporary := filepath.Join(root, fmt.Sprintf(".%s.pemcast-new", filepath.Base(name)))
	_ = os.Remove(temporary)
	if err := os.Symlink(target, temporary); err != nil {
		return err
	}
	defer func() { _ = os.Remove(temporary) }()
	if err := os.Rename(temporary, filepath.Join(root, name)); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(filepath.Join(root, name)))
}

func pruneReleases(root, active string, retain int) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	type release struct {
		name    string
		modTime int64
	}
	var releases []release
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == active || !strings.HasPrefix(entry.Name(), "sha256-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		releases = append(releases, release{name: entry.Name(), modTime: info.ModTime().UnixNano()})
	}
	sort.Slice(releases, func(i, j int) bool { return releases[i].modTime > releases[j].modTime })
	for _, release := range releases[min(retain, len(releases)):] {
		if err := os.RemoveAll(filepath.Join(root, release.name)); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectoryTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return syncDirectory(path)
		}
		return nil
	})
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
