package xtractr

/* Code to write extracted archive members to disk. */

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type file struct {
	Path     string
	Data     io.Reader
	FileMode os.FileMode
	DirMode  os.FileMode
	Mtime    time.Time
	Atime    time.Time
	// Linkname is an explicit symlink target when the archive format stores it
	// outside the file payload (e.g. RAR5 redirection records).
	Linkname string
}

// cleanup runs after a successful extract.
// The intent is to move files into their final location.
func (x *XFile) cleanup(files []string) ([]string, error) {
	files, err := x.squashRoot(files)
	if err != nil {
		return files, err
	}

	return files, nil
}

func (x *XFile) squashRoot(files []string) ([]string, error) {
	if !x.SquashRoot {
		return files, nil
	}

	roots := map[string]struct{}{}

	for _, path := range files {
		// Remove the output dir suffix, then split on `/` (or `\`) and get the first item.
		newRoot := strings.TrimLeft(strings.TrimPrefix(path, x.OutputDir), string(filepath.Separator))
		roots[strings.SplitN(newRoot, string(filepath.Separator), 2)[0]] = struct{}{} //nolint:mnd
	}

	if len(roots) != 1 {
		return files, nil
	}

	for root := range roots {
		from := filepath.Join(x.OutputDir, root)

		info, err := os.Stat(from)
		if err != nil {
			return files, fmt.Errorf("stat squash root: %w", err)
		}

		if !info.IsDir() {
			// A lone top-level file is already in OutputDir. Moving it onto
			// itself used to skip the rename and then delete the extract.
			return files, nil
		}

		return x.moveFiles(from, x.OutputDir, false)
	}

	return files, nil
}

func (x *XFile) safeDirMode(current os.FileMode) os.FileMode {
	if current.Perm() == 0 {
		return x.DirMode
	}

	const minimum = 0o700 // ensure owner has read/write/exec on folders.

	return current | minimum
}

func (x *XFile) safeFileMode(current os.FileMode) os.FileMode {
	if current.Perm() == 0 {
		return x.FileMode
	}

	const minimum = 0o400 // ensure owner has read access to the file.

	return current | minimum
}

func openStatFile(path string) (*os.File, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("os.Open: %w", err)
	}

	stat, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("file.Stat: %w", err)
	}

	return file, stat, nil
}

// mkDir creates a folder (and parents) with safe permissions.
// It refuses to leave the output folder through a pre-existing symlink.
func (x *XFile) mkDir(path string, mode os.FileMode, mtime time.Time) error {
	// Check before creating so we do not mkdir (or Chtimes) through a symlink
	// that already points outside OutputDir.
	if !x.resolvedWithinOutput(path) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, path)
	}

	err := x.mkdirAllCounted(path, mode)
	if err != nil {
		return err
	}

	// Recheck after create: a race could still land the new folder outside OutputDir.
	if !x.resolvedWithinOutput(path) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, path)
	}

	_ = os.Chtimes(path, time.Time{}, mtime)

	return nil
}

// mkdirAllCounted creates each missing component under OutputDir with os.Mkdir
// so MaxFiles charges one slot per new directory. EEXIST from a parallel worker
// is not counted. The caller-provided OutputDir itself is created if needed
// but is not charged.
func (x *XFile) mkdirAllCounted(path string, mode os.FileMode) error {
	path = filepath.Clean(path)
	base := filepath.Clean(x.OutputDir)
	perm := x.safeDirMode(mode)

	err := os.MkdirAll(base, perm)
	if err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	for _, dir := range dirComponentsUnder(base, path) {
		err := x.mkdirComponent(dir, perm)
		if err != nil {
			return err
		}
	}

	return nil
}

// mkdirComponent reserves a MaxFiles slot, then creates dir. Reservation is
// rolled back on EEXIST (another worker or a pre-existing path) so parallel
// ZIP/7z extracts cannot leave an over-limit directory that can no longer be
// removed. A non-directory occupying the path is an error.
func (x *XFile) mkdirComponent(dir string, perm os.FileMode) error {
	err := x.countExtracted()
	if err != nil {
		return err
	}

	err = os.Mkdir(dir, perm)
	switch {
	case err == nil:
		if !x.resolvedWithinOutput(dir) {
			_ = os.Remove(dir)

			x.uncountExtracted()

			return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, dir)
		}

		return nil
	case errors.Is(err, os.ErrExist):
		x.uncountExtracted()

		return x.existingDirOK(dir)
	default:
		x.uncountExtracted()

		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
}

func (x *XFile) existingDirOK(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat existing path %s: %w", dir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%s: %w: %s", x.FilePath, errNotDirectory, dir)
	}

	if !x.resolvedWithinOutput(dir) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, dir)
	}

	return nil
}

// dirComponentsUnder returns cleaned descendants of base on the path from base
// to path, parents first. path must be base or a descendant (see pathWithin).
func dirComponentsUnder(base, path string) []string {
	if !pathWithin(base, path) || path == base {
		return nil
	}

	var stack []string

	for path != base {
		parent := filepath.Dir(path)
		if parent == path {
			break
		}

		stack = append(stack, path)
		path = parent
	}

	slices.Reverse(stack)

	return stack
}

// write a file from an io reader, making sure all parent directories exist.
// Set parallel to true when writing from concurrent workers to throttle progress callbacks.
func (x *XFile) write(file *file) (uint64, error) {
	return x.writeFile(file, false)
}

func (x *XFile) writeParallel(file *file) (uint64, error) {
	return x.writeFile(file, true)
}

func (x *XFile) writeFile(file *file, parallel bool) (uint64, error) {
	err := x.mkDir(filepath.Dir(file.Path), file.DirMode, file.Mtime)
	if err != nil {
		return 0, fmt.Errorf("writing archived file '%s' parent folder: %w", filepath.Base(file.Path), err)
	}

	// ZIP/RAR/7z (and similar) store symlink targets as the member payload with
	// ModeSymlink set. Writing them as regular files leaves a text stub instead
	// of a real link — the same class of bug as tar (#153), different symptom.
	if file.FileMode&os.ModeSymlink != 0 {
		err := x.writeSymlink(file)
		if errors.Is(err, errSkipEntry) {
			return 0, nil
		}

		return 0, err
	}

	fout, pathUsed, err := openExtractFile(file.Path, x.safeFileMode(file.FileMode))
	if err != nil {
		return 0, err
	}

	file.Path = pathUsed

	progWriter, err := x.wrapExtractWriter(fout, parallel)
	if err != nil {
		_ = fout.Close()
		_ = os.Remove(file.Path)

		return 0, err
	}

	size, err := io.Copy(progWriter, file.Data)
	closeErr := fout.Close()

	if err != nil {
		_ = os.Remove(file.Path)

		return uint64(size), fmt.Errorf("copying archived file '%s' io: %w", file.Path, err)
	}

	if closeErr != nil {
		_ = os.Remove(file.Path)

		return uint64(size), fmt.Errorf("closing archived file '%s': %w", file.Path, closeErr)
	}

	// The error is ignored because it's not critical and pops up on OSes like Windows.
	_ = os.Chtimes(file.Path, file.Atime, file.Mtime)

	return uint64(size), nil
}

// extractOpenAttempts bounds the create/replace loop so a tight symlink-plant
// race cannot spin forever. Each pass either returns a regular file or fails closed.
const extractOpenAttempts = 8

// noFollowOpen opens a path for extract writes without following a final-component
// symlink. Production uses openFileNoFollow; tests inject a fake to exercise races.
type noFollowOpen func(path string, flags int, mode os.FileMode) (*os.File, error)

// openExtractFile opens path for writing extracted data without following a
// final-component symlink. The returned path is the one that was actually
// opened (it may be truncated for NAME_MAX).
//
// There is no Lstat-then-open decision. Each attempt exclusively-creates with
// O_NOFOLLOW (Unix) or CREATE_NEW + FILE_FLAG_OPEN_REPARSE_POINT (Windows). If
// the name exists, the existing object is opened the same no-follow way and
// truncated only after the fd is confirmed to be a regular disk file. A symlink
// or directory junction (ELOOP / reparse point) is unlinked and the exclusive
// create is retried.
// Missing-file races retry the exclusive create. Nothing is written through a
// link, device, pipe, or directory.
//
// A hard link to a file outside the output directory is indistinguishable from
// a regular file after open and is still truncated; that matches os.OpenFile.
func openExtractFile(path string, mode os.FileMode) (*os.File, string, error) {
	return openExtractFileWith(openFileNoFollow, path, mode)
}

func openExtractFileWith(open noFollowOpen, path string, mode os.FileMode) (*os.File, string, error) {
	usedPath := path

	var lastErr error

	for range extractOpenAttempts {
		file, openedPath, err := tryOpenExtractFile(open, usedPath, mode)
		if openedPath != "" {
			usedPath = openedPath
		}

		if err == nil {
			return file, usedPath, nil
		}

		lastErr = err

		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if !errors.Is(err, errExtractSymlink) {
			return nil, usedPath, err
		}

		err = os.Remove(usedPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, usedPath, fmt.Errorf("removing symlink at archived file path '%s': %w", usedPath, err)
		}
	}

	if errors.Is(lastErr, os.ErrNotExist) {
		return nil, usedPath, lastErr
	}

	return nil, usedPath, fmt.Errorf("%w: %w", errExtractConflict, lastErr)
}

func tryOpenExtractFile(open noFollowOpen, path string, mode os.FileMode) (*os.File, string, error) {
	file, used, err := openFileNoFollowTrunc(open, path, os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
	if err == nil {
		return finishExtractOpen(file, used, false)
	}

	if errors.Is(err, errExtractSymlink) {
		return nil, used, err
	}

	if !errors.Is(err, os.ErrExist) && !isDeniedExclusiveCreate(err) {
		return nil, used, err
	}

	file, used, err = openFileNoFollowTrunc(open, used, os.O_RDWR, mode)
	if err != nil {
		return nil, used, err
	}

	return finishExtractOpen(file, used, true)
}

func finishExtractOpen(file *os.File, used string, truncate bool) (*os.File, string, error) {
	err := requireRegularFile(file)
	if err != nil {
		_ = file.Close()

		return nil, used, err
	}

	if !truncate {
		return file, used, nil
	}

	err = file.Truncate(0)
	if err != nil {
		_ = file.Close()

		return nil, used, fmt.Errorf("truncating extract file '%s': %w", used, err)
	}

	return file, used, nil
}

func requireRegularFile(file *os.File) error {
	err := requireDiskFile(file)
	if err != nil {
		return err
	}

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat extract file: %w", err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errExtractNotRegular, file.Name())
	}

	return nil
}

// openFileNoFollowTrunc opens path without following a final-component symlink.
// If the path exceeds NAME_MAX, it is truncated and retried, same as openFile.
func openFileNoFollowTrunc(open noFollowOpen, path string, flags int, mode os.FileMode) (*os.File, string, error) {
	file, err := open(path, flags, mode)
	if err == nil {
		return file, path, nil
	}

	if !IsErrNameTooLong(err) {
		return nil, path, err
	}

	shortPath, truncErr := TruncatePathForFS(path)
	if truncErr != nil {
		return nil, "", truncErr
	}

	file, err = open(shortPath, flags, mode)
	if err != nil {
		return nil, shortPath, err
	}

	return file, shortPath, nil
}

// writeExtractFile writes data to path without following a final-component
// symlink. Used for non-archive output (CUE copy, embedded pictures) that
// otherwise goes through os.WriteFile, which follows links.
func writeExtractFile(path string, data []byte, mode os.FileMode) error {
	return (&XFile{}).writeExtractFile(path, data, mode)
}

func (x *XFile) writeExtractFile(path string, data []byte, mode os.FileMode) error {
	fout, usedPath, err := openExtractFile(path, mode)
	if err != nil {
		return err
	}

	writer, err := x.extractWriter(fout)
	if err != nil {
		_ = fout.Close()
		_ = os.Remove(usedPath)

		return err
	}

	_, err = writer.Write(data)
	closeErr := fout.Close()

	if err != nil {
		_ = os.Remove(usedPath)

		return fmt.Errorf("writing file '%s': %w", usedPath, err)
	}

	if closeErr != nil {
		_ = os.Remove(usedPath)

		return fmt.Errorf("closing file '%s': %w", usedPath, closeErr)
	}

	return nil
}

// maxSymlinkTarget is the maximum bytes allowed for a symlink target read from
// an archive member payload. Prevents a ModeSymlink entry with a huge payload
// from exhausting memory.
const maxSymlinkTarget = 8 * 1024

// writeSymlink reads a symlink target and creates the link at file.Path.
// Prefer file.Linkname when set (RAR5 redirections); otherwise read file.Data
// (ZIP/7z store the target as the member payload).
func (x *XFile) writeSymlink(file *file) error {
	linkName := file.Linkname
	if linkName == "" && file.Data != nil {
		limited := io.LimitReader(file.Data, maxSymlinkTarget+1)

		raw, err := io.ReadAll(limited)
		if err != nil {
			return fmt.Errorf("reading archived symlink '%s' target: %w", file.Path, err)
		}

		if len(raw) > maxSymlinkTarget {
			return fmt.Errorf("%s: %w: %s", x.FilePath, ErrSymlinkTooLong, file.Path)
		}

		linkName = strings.TrimRight(string(raw), "\x00")
	}

	if linkName == "" {
		x.Printf("Warning: skipping symlink with empty target: %s", file.Path)

		return errSkipEntry
	}

	err := os.Remove(file.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s: removing existing path for symlink: %w: %s", x.FilePath, err, file.Path)
	}

	return x.createSymlink(file.Path, linkName)
}

// clean returns an absolute path for a file inside the OutputDir.
// If trim length is > 0, then the suffixes are trimmed, and filepath removed.
func (x *XFile) clean(filePath string, trim ...string) string {
	if len(trim) != 0 {
		filePath = filepath.Base(filePath)
		for _, suffix := range trim {
			filePath = strings.TrimSuffix(filePath, suffix)
		}
	}

	return filepath.Clean(filepath.Join(x.OutputDir, filePath))
}

// pathWithin reports whether target is base or a descendant of it.
// Uses filepath.Rel so sibling-prefix tricks like base=/tmp/out and
// target=/tmp/out_evil fail (unlike strings.HasPrefix).
func pathWithin(base, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(base), filepath.Clean(target))
	if err != nil {
		return false
	}

	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// pathWithinOutput reports whether path is OutputDir or a descendant of it,
// comparing the cleaned paths lexically.
func (x *XFile) pathWithinOutput(path string) bool {
	return pathWithin(x.OutputDir, path)
}

// resolveExisting resolves symlinks in the deepest existing portion of path,
// then re-appends the not-yet-created tail. This normalizes a path for
// containment checks when some of its components may not exist yet, or when
// the path itself lives behind a symlink (e.g. /var -> /private/var on macOS).
// If symlink resolution fails, the cleaned path is returned unchanged.
func resolveExisting(path string) string {
	probe := filepath.Clean(path)
	tail := []string{}

	for {
		_, err := os.Lstat(probe)
		if err == nil {
			break
		}

		parent := filepath.Dir(probe)
		if parent == probe {
			return probe // reached the filesystem root; nothing exists to resolve.
		}

		tail = append([]string{filepath.Base(probe)}, tail...)
		probe = parent
	}

	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		resolved = probe
	}

	for _, segment := range tail {
		resolved = filepath.Join(resolved, segment)
	}

	return resolved
}

// resolvedWithinOutput reports whether path stays inside OutputDir after
// resolving symlinks in the existing portions of both paths. The lexical
// check alone is not enough: a symlink already present in the output folder
// (planted by a previous download, another app, or an attacker) is followed
// by os.MkdirAll and os.OpenFile, writing files outside the output folder.
func (x *XFile) resolvedWithinOutput(path string) bool {
	return pathWithin(resolveExisting(x.OutputDir), resolveExisting(path))
}

// resolveLinkTarget returns the cleaned filesystem path a link would resolve to.
func resolveLinkTarget(linkPath, linkName string) string {
	if filepath.IsAbs(linkName) {
		return filepath.Clean(linkName)
	}

	return filepath.Clean(filepath.Join(filepath.Dir(linkPath), linkName))
}

// ensureLinkWithinOutput rejects symlink targets that escape OutputDir,
// including those that only escape after following a pre-existing symlink.
func (x *XFile) ensureLinkWithinOutput(linkPath, linkName string) error {
	resolved := resolveLinkTarget(linkPath, linkName)
	if !x.pathWithinOutput(resolved) || !x.resolvedWithinOutput(resolved) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, resolved, linkName)
	}

	return nil
}

func (x *XFile) createSymlink(path, linkName string) error {
	if linkName == "" {
		x.Printf("Warning: skipping symlink with empty target: %s", path)

		return errSkipEntry
	}

	err := x.ensureLinkWithinOutput(path, linkName)
	if err != nil {
		return err
	}

	x.Debugf("Writing archived symlink: %s -> %s", path, linkName)

	err = os.Symlink(linkName, path)
	if err != nil {
		return fmt.Errorf("%s: creating symlink: %w: %s -> %s", x.FilePath, err, path, linkName)
	}

	err = x.countExtracted()
	if err != nil {
		_ = os.Remove(path)
		return err
	}

	return nil
}

func (x *XFile) createHardLink(path, linkName string) error {
	if linkName == "" {
		x.Printf("Warning: skipping hard link with empty target: %s", path)

		return errSkipEntry
	}

	// Hard-link names are archive member paths, not arbitrary filesystem paths.
	if filepath.IsAbs(linkName) {
		return fmt.Errorf("%s: %w: %s", x.FilePath, ErrInvalidPath, linkName)
	}

	target := x.clean(linkName)
	if !x.pathWithinOutput(target) || !x.resolvedWithinOutput(target) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, target, linkName)
	}

	x.Debugf("Writing archived hard link: %s => %s", path, target)

	err := os.Link(target, path)
	if err == nil {
		countErr := x.countExtracted()
		if countErr != nil {
			_ = os.Remove(path)
			return countErr
		}

		return nil
	}

	linkErr := err

	rel, relErr := filepath.Rel(filepath.Dir(path), target)
	if relErr != nil {
		return fmt.Errorf("%s: creating hard link: %w: %s => %s", x.FilePath, linkErr, path, target)
	}

	// Fall back to a relative symlink when hard links are unavailable
	// (e.g. target not extracted yet, or the filesystem does not support them).
	x.Debugf("Hard link failed (%v); falling back to symlink: %s -> %s", linkErr, path, rel)

	return x.createSymlink(path, rel)
}
