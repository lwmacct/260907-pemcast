// Package deploy atomically activates verified bundles using release directories.
package deploy

import (
	"bytes"
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

// CurrentDigest reads the content identity stored in the selected release and
// rejects a current path that is not a safely scoped managed symlink.
func (d *Deployer) CurrentDigest(output config.Output) (string, error) {
	linkPath := filepath.Join(output.Root, output.CurrentLink)
	info, err := os.Lstat(linkPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", fmt.Errorf("current path is not a symlink")
	}
	target, err := os.Readlink(linkPath)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", fmt.Errorf("current symlink target %q must be clean and relative", target)
	}
	release, ok := managedReleaseTarget(target)
	if !ok {
		return "", fmt.Errorf("current symlink target %q is outside managed releases", target)
	}
	if !strings.HasPrefix(release, "sha256-") || release == "sha256-" || strings.ContainsAny(release, `/\`) {
		return "", fmt.Errorf("current symlink target %q is not a content-addressed release", target)
	}
	wantDigest := strings.TrimPrefix(release, "sha256-")
	data, err := os.ReadFile(filepath.Join(linkPath, ".pemcast-digest"))
	if err != nil {
		return "", err
	}
	digest := strings.TrimSpace(string(data))
	if digest == "" || digest != wantDigest {
		return "", fmt.Errorf("current release digest %q does not match symlink release %q", digest, wantDigest)
	}
	return digest, nil
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
		releaseDir := filepath.Join(output.Root, managedDirectory, "releases", "sha256-"+material.Digest)
		if err := verifyRelease(releaseDir, material, output); err != nil {
			return Result{}, fmt.Errorf("verify active release: %w", err)
		}
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
	if info, err := os.Lstat(releaseDir); err == nil && info.IsDir() {
		return verifyRelease(releaseDir, material, output)
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
			return verifyRelease(releaseDir, material, output)
		}
		return fmt.Errorf("commit release directory: %w", err)
	}
	return syncDirectory(filepath.Dir(releaseDir))
}

func managedReleaseTarget(target string) (string, bool) {
	prefix := filepath.Join(managedDirectory, "releases") + string(filepath.Separator)
	release, ok := strings.CutPrefix(target, prefix)
	return release, ok && release != "." && release != ".."
}

func verifyRelease(releaseDir string, material *bundle.Material, output config.Output) error {
	if material == nil {
		return fmt.Errorf("bundle material is nil")
	}
	digestPath := filepath.Join(releaseDir, ".pemcast-digest")
	digestData, err := os.ReadFile(digestPath)
	if err != nil {
		return fmt.Errorf("read release digest: %w", err)
	}
	if strings.TrimSpace(string(digestData)) != material.Digest {
		return fmt.Errorf("release digest mismatch: got %q want %q", strings.TrimSpace(string(digestData)), material.Digest)
	}
	digestInfo, err := os.Lstat(digestPath)
	if err != nil {
		return fmt.Errorf("stat release digest: %w", err)
	}
	if !digestInfo.Mode().IsRegular() || digestInfo.Mode().Perm() != 0o600 {
		return fmt.Errorf("release digest must be a regular file with mode 0600")
	}

	expected := map[string]fs.FileMode{".pemcast-digest": 0o600}
	expectedDirs := make(map[string]struct{})
	for _, mapping := range output.Mappings {
		relative := filepath.FromSlash(mapping.Local)
		if _, duplicate := expected[relative]; duplicate {
			return fmt.Errorf("duplicate local mapping %q", relative)
		}
		mode := mapping.Mode.Perm()
		if mode == 0 {
			return fmt.Errorf("mapping %q has zero mode", relative)
		}
		expected[relative] = mode
		for directory := filepath.Dir(relative); directory != "."; directory = filepath.Dir(directory) {
			expectedDirs[directory] = struct{}{}
		}
		content := material.Files[mapping.Remote]
		path := filepath.Join(releaseDir, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("stat mapped file %q: %w", relative, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("mapped file %q is not a regular file", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read mapped file %q: %w", relative, err)
		}
		if !bytes.Equal(data, content) {
			return fmt.Errorf("mapped file %q content mismatch", relative)
		}
		if info.Mode().Perm() != expected[relative] {
			return fmt.Errorf("mapped file %q mode mismatch: got %o want %o", relative, info.Mode().Perm(), expected[relative])
		}
	}

	seen := make(map[string]struct{}, len(expected))
	err = filepath.WalkDir(releaseDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(releaseDir, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode().Perm() != output.DirectoryMode.Perm() {
				return fmt.Errorf("release root mode mismatch: got %o want %o", info.Mode().Perm(), output.DirectoryMode.Perm())
			}
			return nil
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().Perm() != output.DirectoryMode.Perm() {
				return fmt.Errorf("release directory %q mode mismatch: got %o want %o", relative, info.Mode().Perm(), output.DirectoryMode.Perm())
			}
			if _, expected := expectedDirs[filepath.FromSlash(relative)]; !expected {
				return fmt.Errorf("release contains unexpected directory %q", relative)
			}
			return nil
		}
		if _, ok := expected[filepath.FromSlash(relative)]; !ok {
			return fmt.Errorf("release contains unexpected entry %q", relative)
		}
		if _, duplicate := seen[relative]; duplicate {
			return fmt.Errorf("release entry %q visited more than once", relative)
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected) {
		return fmt.Errorf("release contains %d expected files, want %d", len(seen), len(expected))
	}
	return nil
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
	return errors.Join(directory.Sync(), directory.Close())
}
