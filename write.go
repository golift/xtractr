package xtractr

/* Code to write extracted archive members to disk. */

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// linkWrite serializes symlink and hard-link writes across workers, and keeps
// them out of the span in which a file's parent folders are checked and the
// file is opened. Zip, 7z, and ASAR write entries concurrently. The checks
// have to see every link written before them, and a link written between
// mkDir and the open, even one the audit then rolls back, is what the open
// would follow. Link writes take it exclusively; openOutputFile shares it.
//
//nolint:gochecknoglobals // password retries copy XFile, so this lock cannot live on it.
var linkWrite sync.RWMutex

type file struct {
	Path     string
	Data     io.Reader
	FileMode os.FileMode
	Mtime    time.Time
	Atime    time.Time
	// Linkname is an explicit symlink target when the archive format stores it
	// outside the file payload (e.g. RAR5 redirection records).
	Linkname string
}

// cleanup runs after a successful extract.
// The intent is to move files into their final location.
func (x *XFile) cleanup(files []string) ([]string, error) {
	// Tar records header names; every other extractor records paths under
	// OutputDir. Squash moves from a child of OutputDir, so resolve against
	// OutputDir here. The returned list stays as the extractor built it.
	x.moveKnown = resolveExtractPaths(x.OutputDir, files)

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
		// Remove the output dir prefix, then split on the separator and get the
		// first item. Tar records header names, which use "/" on every OS;
		// without FromSlash, Windows treats "pkg/a.txt" as one root, stats the
		// file, and skips the move because it is not a directory.
		path = filepath.FromSlash(path)
		newRoot := strings.TrimLeft(strings.TrimPrefix(path, x.OutputDir), string(filepath.Separator))
		roots[strings.SplitN(newRoot, string(filepath.Separator), 2)[0]] = struct{}{} //nolint:mnd
	}

	if len(roots) != 1 {
		return files, nil
	}

	for root := range roots {
		from := filepath.Join(x.OutputDir, root)

		info, err := os.Lstat(from)
		if err != nil {
			return files, fmt.Errorf("stat squash root: %w", err)
		}

		if !info.IsDir() {
			// A lone top-level file is already in OutputDir. Moving it onto
			// itself used to skip the rename and then delete the extract. A
			// lone symlink stays as well: the move would follow it and pull
			// the contents of whatever directory it names into OutputDir.
			return files, nil
		}

		err = x.ensureLinksWithin(from)
		if err != nil {
			return files, fmt.Errorf("%s: %w", x.FilePath, err)
		}

		// ExtractFile binds this. A direct ExtractTarGzip (or any other
		// Extract* call) does not, and a nil moveFiles panics here.
		// Called through a variable: a direct call to bindMoveFiles closes an
		// initialization cycle through IsArchiveFile and extension2function.
		ensureMoveFiles(x)

		return x.moveFiles(from, x.OutputDir, false)
	}

	return files, nil
}

// Permission triads. FileMode supplies read and write. When PreserveExec is set,
// an execute bit stored in the archive is kept only for a triad FileMode already
// grants some access, so a 6 beside an archive 7 becomes 7, a 4 becomes 5, and a
// 0 stays 0. DirMode is used as given. Umask still applies at open. A zero
// configured mode uses the package default. Setuid, setgid, and sticky are discarded.
const (
	ownerPermMask   os.FileMode = 0o700
	groupPermMask   os.FileMode = 0o070
	otherPermMask   os.FileMode = 0o007
	executePermMask os.FileMode = 0o111
)

// permMode keeps the read, write, and execute bits. Setuid, setgid, and sticky
// are discarded. A zero permission uses fallback.
func permMode(mode, fallback os.FileMode) os.FileMode {
	mode = mode.Perm()
	if mode == 0 {
		return fallback
	}

	return mode
}

// fileMode is the mode passed to open. When PreserveExec is set, archive
// execute bits are kept for each triad this mode already grants.
func (x *XFile) fileMode(archive os.FileMode) os.FileMode {
	mode := permMode(x.FileMode, DefaultFileMode)
	if !x.PreserveExec {
		return mode
	}

	return mode | preservedExec(archive, mode)
}

// dirMode is the mode passed to mkdir.
func (x *XFile) dirMode() os.FileMode {
	return permMode(x.DirMode, DefaultDirMode)
}

// preservedExec returns archive execute bits for triads that configured already allows.
func preservedExec(archive, configured os.FileMode) os.FileMode {
	exec := archive.Perm() & executePermMask
	if configured&ownerPermMask == 0 {
		exec &^= ownerPermMask & executePermMask
	}

	if configured&groupPermMask == 0 {
		exec &^= groupPermMask & executePermMask
	}

	if configured&otherPermMask == 0 {
		exec &^= otherPermMask & executePermMask
	}

	return exec
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
func (x *XFile) mkDir(path string, mtime time.Time) error {
	// Check before creating so we do not mkdir (or Chtimes) through a symlink
	// that already points outside OutputDir.
	if !x.resolvedWithinOutput(path) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, path)
	}

	err := x.mkdirAllCounted(path)
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
func (x *XFile) mkdirAllCounted(path string) error {
	path = filepath.Clean(path)
	base := filepath.Clean(x.OutputDir)
	perm := x.dirMode()

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

// writeFile writes one archive member. A partial file is removed here, and
// callers must not remove file.Path on error themselves: the path may be a
// symlink entry that was left or put back, or a path whose parent resolves
// outside OutputDir, which os.Remove would follow.
func (x *XFile) writeFile(file *file, parallel bool) (uint64, error) {
	// ZIP/RAR/7z (and similar) store symlink targets as the member payload with
	// ModeSymlink set. Writing them as regular files leaves a text stub instead
	// of a real link — the same class of bug as tar (#153), different symptom.
	if file.FileMode&os.ModeSymlink != 0 {
		err := x.mkDir(filepath.Dir(file.Path), file.Mtime)
		if err != nil {
			return 0, fmt.Errorf("writing archived file '%s' parent folder: %w", filepath.Base(file.Path), err)
		}

		err = x.writeSymlink(file)
		if errors.Is(err, errSkipEntry) {
			return 0, nil
		}

		return 0, err
	}

	fout, pathUsed, err := x.openOutputFile(file)
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

	x.noteArchiveOutput(file.Path, uint64(size))

	// The error is ignored because it's not critical and pops up on OSes like Windows.
	_ = os.Chtimes(file.Path, file.Atime, file.Mtime)

	return uint64(size), nil
}

// openOutputFile makes the file's parent folders and opens it while no link is
// being written (linkWrite). mkDir checks that the parents resolve inside
// OutputDir; the open follows whatever the parents are at that moment, so the
// two have to see the same links.
func (x *XFile) openOutputFile(file *file) (*os.File, string, error) {
	linkWrite.RLock()
	defer linkWrite.RUnlock()

	err := x.mkDir(filepath.Dir(file.Path), file.Mtime)
	if err != nil {
		return nil, "", fmt.Errorf("writing archived file '%s' parent folder: %w", filepath.Base(file.Path), err)
	}

	return openExtractFile(file.Path, x.fileMode(file.FileMode))
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
	return openExtractFileWith(openFileNoFollow, path, permMode(mode, DefaultFileMode))
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

		if !truncate {
			_ = os.Remove(used) // the exclusive create made this file.
		}

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

	// An entry already at this path is replaced inside createSymlink, after
	// the text is checked, so a rejected link leaves it in place.
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

// maxSymlinkHops is the total number of links walked for one archive symlink,
// counting every branch. A depth cap is not enough: a target like s1/s1, where
// each link points at two copies of the previous one, does exponential work
// while staying shallow. Cycles and that fan-out fail closed.
const maxSymlinkHops = 40

// linkWalk is the state of one link resolution. followed counts every link
// followed, across all branches, against maxSymlinkHops.
type linkWalk struct {
	followed int
	// visits are the paths looked up, in order. A link written at one of
	// them later changes where this resolution lands. See plantedLink.
	visits []string
	// rejectClimbs fails the walk at a followed link whose own text climbs
	// after a component. That is a rule for text the archive asks xtractr to
	// write, not part of resolving a link that already exists, so checks on
	// those leave it off. See followLinkElem.
	rejectClimbs bool
	// onDisk, when set, maps a path the walk reaches to where that entry is
	// right now. SquashRoot checks links at the position they will have after
	// the move while the entries are still under the root. See squashLayout.
	onDisk func(path string) string
}

func (w *linkWalk) consume() error {
	if w.followed >= maxSymlinkHops {
		return fmt.Errorf("%w: symlink cycle", ErrInvalidPath)
	}

	w.followed++

	return nil
}

func (w *linkWalk) disk(path string) string {
	if w.onDisk == nil {
		return path
	}

	return w.onDisk(path)
}

// linkTargetStart returns the directory a link's text is resolved from and
// the text left to walk. Absolute text starts at its volume root. On Windows,
// rooted text ("\foo") starts at the root of the volume holding the link, and
// drive-relative text ("C:foo") is rejected because it depends on a per-drive
// working directory.
func linkTargetStart(parent, target string) (string, string, error) {
	target = slashToSeparator(target)

	switch {
	case filepath.IsAbs(target):
		return volumeRoot(target), strings.TrimPrefix(target, filepath.VolumeName(target)), nil
	case filepath.VolumeName(target) != "":
		return "", "", fmt.Errorf("%w: drive-relative link target: %s", ErrInvalidPath, target)
	case strings.HasPrefix(target, string(os.PathSeparator)):
		return volumeRoot(parent), target, nil
	}

	return parent, target, nil
}

// walkLinkTarget returns the path the kernel would resolve for a symlink
// created at linkPath with target text linkName.
//
// filepath.Clean is the wrong tool here: it drops ".." lexically, so a target
// like foo/bar/../outside still looks inside OutputDir when foo/bar is a
// symlink to "..". This walk applies ".." to the directory actually reached,
// following links that already exist. A component that does not exist yet is
// kept as written.
func walkLinkTarget(linkPath, linkName string, walk *linkWalk) (string, error) {
	start, rest, err := linkTargetStart(resolveExisting(filepath.Dir(linkPath)), linkName)
	if err != nil {
		return "", err
	}

	return walkLinkElems(start, splitElems(rest), walk)
}

func walkLinkElems(dir string, elems []string, walk *linkWalk) (string, error) {
	current := dir

	for _, elem := range elems {
		next, err := stepLinkElem(current, elem, walk)
		if err != nil {
			return "", err
		}

		current = next
	}

	return current, nil
}

func stepLinkElem(current, elem string, walk *linkWalk) (string, error) {
	switch elem {
	case "", ".":
		return current, nil
	case "..":
		// The kernel climbs out of directories only; ".." after a regular
		// file is ENOTDIR and the link does not resolve. Climbing anyway
		// reports a path for text like file/../../victim, outside OutputDir,
		// and the planted-link collection would drop that link as already
		// outside instead of keeping it for the write that replaces the file.
		err := climbableDir(walk.disk(current))
		if err != nil {
			return "", err
		}

		// filepath.Dir(".") stays "." and filepath.Dir("..") stays ".", so a
		// relative OutputDir would drop climbs and then look contained.
		// Join preserves them: Join(".", "..") is ".." and Join("..", "..")
		// is "../..".
		return filepath.Join(current, ".."), nil
	}

	next := filepath.Join(current, elem)
	walk.visits = append(walk.visits, next)

	info, err := os.Lstat(walk.disk(next))
	if errors.Is(err, os.ErrNotExist) {
		return next, nil
	}

	if err != nil {
		return "", fmt.Errorf("lstat %s: %w", next, err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		return next, nil
	}

	return followLinkElem(current, next, walk)
}

// climbableDir fails when path exists and is not a directory. A missing path
// passes: a component that does not exist yet is climbed lexically, and the
// climb-after rule and the planted-link lookups cover what is created there.
func climbableDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("lstat %s: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("climb from %s: %w", path, syscall.ENOTDIR)
	}

	return nil
}

func followLinkElem(parent, link string, walk *linkWalk) (string, error) {
	err := walk.consume()
	if err != nil {
		return "", err
	}

	target, err := os.Readlink(walk.disk(link))
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", link, err)
	}

	// The archive entry's own text may be a single name. The ".." that escapes
	// can live in this existing link, which a later entry can arm by creating
	// the missing component as a shallower symlink.
	if walk.rejectClimbs && linkTargetClimbsAfterComponent(target) {
		return "", fmt.Errorf("%w: %s -> %s", ErrInvalidPath, link, target)
	}

	start, rest, err := linkTargetStart(parent, target)
	if err != nil {
		return "", err
	}

	return walkLinkElems(start, splitElems(rest), walk)
}

func slashToSeparator(path string) string {
	if os.PathSeparator == '/' {
		return path
	}

	return strings.ReplaceAll(path, "/", string(os.PathSeparator))
}

func splitElems(path string) []string {
	return strings.Split(path, string(os.PathSeparator))
}

func volumeRoot(path string) string {
	vol := filepath.VolumeName(path)
	if vol == "" {
		return string(os.PathSeparator)
	}

	if strings.HasSuffix(vol, string(os.PathSeparator)) {
		return vol
	}

	return vol + string(os.PathSeparator)
}

// lexicalLinkTarget is where the link text points after dropping "." and "..",
// without following any symlink. A walk can still accept that text when an
// existing link is deep enough to absorb the climbs; a later entry can shorten
// that link and leave the earlier one pointing outside OutputDir.
func lexicalLinkTarget(linkPath, linkName string) (string, error) {
	start, rest, err := linkTargetStart(filepath.Dir(linkPath), linkName)
	if err != nil {
		return "", err
	}

	return filepath.Clean(filepath.Join(start, rest)), nil
}

// linkTargetClimbsAfterComponent reports whether ".." appears after a normal
// path component. Leading climbs ("../file") only move through ancestors of
// the link, which cannot be removed while the link exists. A climb after a
// component ("foo/../victim") depends on whatever is later created or replaced
// at that name, so the same text can resolve outside OutputDir.
func linkTargetClimbsAfterComponent(linkName string) bool {
	sawComponent := false

	for _, elem := range splitElems(slashToSeparator(linkName)) {
		switch elem {
		case "", ".":
			continue
		case "..":
			if sawComponent {
				return true
			}
		default:
			sawComponent = true
		}
	}

	return false
}

// ensureLinkWithinOutput rejects symlink targets that escape OutputDir,
// including ones that escape only after an earlier symlink is followed.
//
// Three checks, all required. The lexical target keeps a later entry from
// shortening a link this text climbs through. The climb-after rule keeps a
// later entry from creating a component this text climbs through. The walk
// is what the kernel will do with the links on disk right now.
func (x *XFile) ensureLinkWithinOutput(linkPath, linkName string) error {
	if linkTargetClimbsAfterComponent(linkName) {
		return fmt.Errorf("%s: %w: %s", x.FilePath, ErrInvalidPath, linkName)
	}

	lexical, err := lexicalLinkTarget(linkPath, linkName)
	if err != nil {
		return fmt.Errorf("%s: %w", x.FilePath, err)
	}

	if !x.pathWithinOutput(lexical) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, lexical, linkName)
	}

	resolved, err := walkLinkTarget(linkPath, linkName, &linkWalk{rejectClimbs: true, onDisk: x.onDisk})
	if err != nil {
		return fmt.Errorf("%s: %w: %s: %w", x.FilePath, ErrInvalidPath, linkName, err)
	}

	if !pathWithin(resolveExisting(x.OutputDir), resolved) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, resolved, linkName)
	}

	return nil
}

// ensureLinksWithin checks the links SquashRoot is about to move out of root,
// each at the position it will have afterwards and against the layout the
// move produces (squashLayout), while nothing has moved yet.
//
// Checking them where they are is not enough: moving shortens every relative
// link by a component, and the names the move fills in under OutputDir change
// what links already there resolve to. A link that leaves root and comes back
// ("../pkg/victim") lands on a sibling of OutputDir; a link to a name that is
// occupied under OutputDir follows that entry, since the move keeps it; a
// moved link to "." arms a planted "missing/../victim" the same way creating
// it there would, so the climb-after links are audited against the layout too.
func (x *XFile) ensureLinksWithin(root string) error {
	links, err := symlinksUnder(root)
	if err != nil {
		return fmt.Errorf("squash root: %w", err)
	}

	if len(links) == 0 {
		return nil // Files and directories resolve where they are put.
	}

	// Extraction has finished, so no link write races this. The scan is a
	// no-op when the archive wrote a link; links already under root need it.
	x.collectPlantedLinks()

	layout := newSquashLayout(x.OutputDir, root)
	scope := &XFile{
		FilePath:         x.FilePath,
		OutputDir:        x.OutputDir,
		plantedCollected: true,
		onDisk:           layout.onDisk,
	}

	// Links under root are checked below, at the position the move gives
	// them. The planted links elsewhere are walked under the layout.
	moved := resolveExisting(root)

	for _, link := range x.planted {
		if !pathWithin(moved, link.path) {
			scope.planted = append(scope.planted, link)
		}
	}

	for _, path := range links {
		target, err := os.Readlink(path)
		if err != nil {
			return fmt.Errorf("squash root: readlink: %w", err)
		}

		err = scope.ensureLinkWithinOutput(layout.destination(path), target)
		if err != nil {
			return fmt.Errorf("squash root: %w", err)
		}
	}

	err = scope.plantedLinksInside("")
	if err != nil {
		return fmt.Errorf("squash root: %w", err)
	}

	return nil
}

// symlinksUnder lists the symlinks in the tree at root without following any.
func symlinksUnder(root string) ([]string, error) {
	var links []string

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && entry.Type()&os.ModeSymlink != 0 {
			links = append(links, path)
		}

		return walkErr
	})
	if err != nil {
		return nil, fmt.Errorf("listing links: %w", err)
	}

	return links, nil
}

// squashLayout is OutputDir as it will be once SquashRoot moves root's
// children up one level. moveFilesKnown moves the top-level children only and
// refuses an occupied destination: a name that already exists under OutputDir
// keeps that entry, and the copy under root is discarded with root (or left
// there if another move fails). Every other name is whatever is under root.
type squashLayout struct {
	outputDir string
	resolved  string // resolveExisting(outputDir); walks run in this space.
	root      string
}

func newSquashLayout(outputDir, root string) *squashLayout {
	return &squashLayout{outputDir: outputDir, resolved: resolveExisting(outputDir), root: root}
}

// occupied reports whether the move refuses the top-level child named top.
func (l *squashLayout) occupied(top string) bool {
	_, err := os.Lstat(filepath.Join(l.outputDir, top))

	return err == nil
}

// destination is where the entry at path, under root, sits after the move.
func (l *squashLayout) destination(path string) string {
	rel, err := filepath.Rel(l.root, path)
	if err != nil {
		return path
	}

	if top, _, _ := strings.Cut(rel, string(filepath.Separator)); l.occupied(top) {
		return path
	}

	return filepath.Join(l.outputDir, rel)
}

// onDisk is the linkWalk hook: a path under OutputDir whose top-level name the
// move fills in is looked up under root. See linkWalk.
func (l *squashLayout) onDisk(path string) string {
	rel, err := filepath.Rel(l.resolved, path)
	if err != nil || rel == "." || !pathWithin(l.resolved, path) {
		return path
	}

	if top, _, _ := strings.Cut(rel, string(filepath.Separator)); l.occupied(top) {
		return path
	}

	return filepath.Join(l.root, rel)
}

func (x *XFile) createSymlink(path, linkName string) error {
	linkWrite.Lock()
	defer linkWrite.Unlock()

	return x.createSymlinkLocked(path, linkName)
}

func (x *XFile) createSymlinkLocked(path, linkName string) error {
	if linkName == "" {
		x.Printf("Warning: skipping symlink with empty target: %s", path)

		return errSkipEntry
	}

	err := x.ensureLinkWithinOutput(path, linkName)
	if err != nil {
		return err
	}

	old, err := x.replaceEntry(path)
	if err != nil {
		return err
	}

	return x.placeSymlink(path, linkName, old)
}

// placeSymlink writes a symlink whose text passed ensureLinkWithinOutput at
// a path replaceEntry cleared.
func (x *XFile) placeSymlink(path, linkName string, old replacedEntry) error {
	x.Debugf("Writing archived symlink: %s -> %s", path, linkName)

	err := os.Symlink(linkName, path)
	if err != nil {
		old.restore()

		return fmt.Errorf("%s: creating symlink: %w: %s -> %s", x.FilePath, err, path, linkName)
	}

	return x.finishLink(path, old)
}

// finishLink runs once a symlink or hard link exists at path. The write is
// undone, and what it replaced put back, if it exceeds MaxFiles or moved a
// planted link outside OutputDir. The audit goes last: it records what the
// planted links look up now, and that must describe the layout that stays.
func (x *XFile) finishLink(path string, old replacedEntry) error {
	err := x.countExtracted()
	if err == nil {
		err = x.plantedLinksInside(path)
	}

	if err != nil {
		old.restore()

		return err
	}

	return nil
}

// replacedEntry is what a link write found at its path. Archives replace a
// symlink, an empty directory, or a file with a link, so the entry is removed
// first; a write that then fails puts it back, since a removed symlink changes
// what resolves through it just as a new one does.
type replacedEntry struct {
	path   string
	found  bool
	mode   os.FileMode
	target string // symlink text, when the entry was one.
}

// replaceEntry collects the planted links, then clears path for a link.
// Collection comes first: the planted set records where links resolved
// before this extract changed anything, and removing a symlink here is a
// change. The text going in has already been checked, so nothing is removed
// for an entry that will be rejected.
func (x *XFile) replaceEntry(path string) (replacedEntry, error) {
	x.collectPlantedLinks()

	old := replacedEntry{path: path}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return old, nil
	}

	if err != nil {
		return old, fmt.Errorf("%s: checking existing path for link: %w: %s", x.FilePath, err, path)
	}

	old.found = true
	old.mode = info.Mode()

	if old.mode&os.ModeSymlink != 0 {
		old.target, _ = os.Readlink(path)
	}

	err = os.Remove(path)
	if err != nil {
		return old, fmt.Errorf("%s: removing existing path for link: %w: %s", x.FilePath, err, path)
	}

	return old, nil
}

// restore removes the link written at path and puts back the symlink or
// empty directory that was there. A regular file is gone; it was replaced by
// text that passed the checks, and the failure came after that.
func (r replacedEntry) restore() {
	_ = os.Remove(r.path)

	switch {
	case !r.found:
	case r.mode&os.ModeSymlink != 0 && r.target != "":
		_ = os.Symlink(r.target, r.path)
	case r.mode.IsDir():
		_ = os.Mkdir(r.path, r.mode.Perm())
	}
}

// plantedLink is a symlink that was under OutputDir before this extract wrote
// or removed any link, and did not resolve outside it. visits are the paths
// its resolution looked up; a link written at one of them can move the link.
type plantedLink struct {
	path   string
	visits []string
}

func (l *plantedLink) visited(path string) bool {
	for _, visit := range l.visits {
		// Case-insensitive, since the filesystem may be. A spurious match
		// only costs a walk.
		if strings.EqualFold(visit, path) {
			return true
		}
	}

	return false
}

// uniqueVisits drops the repeats a cycle leaves in a walk's lookups.
func uniqueVisits(visits []string) []string {
	slices.Sort(visits)

	return slices.Compact(visits)
}

// collectPlantedLinks finds the symlinks already under OutputDir that do not
// resolve outside it. One of those lands outside once a link is written in
// its path: "missing/../victim" when missing -> . appears, or
// "foo/../../victim" when the foo it climbed through is replaced by a shorter
// link. The archive's own links are judged by their text and never need
// this; a link already in place is judged by where it resolves, whatever its
// text looks like.
//
// A link that does not resolve at all (a cycle, a file in its path) is kept
// as well, with the lookups it made: the write that makes it resolve has to
// be audited too, and a dropped link would be missed. One that already
// resolves outside was pointing out before this extract and is left alone.
//
// Links whose text leaves OutputDir without climbing after a component are
// skipped without a walk: they come back inside only through a link outside
// OutputDir, which this extract never writes. Unreadable entries are
// skipped; the walk is a best-effort defense against an attacker who could
// already write into OutputDir.
func (x *XFile) collectPlantedLinks() {
	if x.plantedCollected {
		return
	}

	x.plantedCollected = true
	root := resolveExisting(x.OutputDir)

	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr == nil && entry.Type()&os.ModeSymlink != 0 {
			if link, ok := plantedLinkAt(root, path); ok {
				x.planted = append(x.planted, link)
			}
		}

		return nil
	})
}

// plantedLinkAt resolves the symlink at path the way the kernel would and
// returns it, with the paths looked up, unless it lands outside root.
func plantedLinkAt(root, path string) (plantedLink, bool) {
	target, err := os.Readlink(path)
	if err != nil {
		return plantedLink{}, false
	}

	lexical, err := lexicalLinkTarget(path, target)
	if err != nil {
		return plantedLink{}, false
	}

	if !pathWithin(root, lexical) && !linkTargetClimbsAfterComponent(target) {
		return plantedLink{}, false
	}

	walk := &linkWalk{}

	resolved, err := walkLinkTarget(path, target, walk)
	if err == nil && !pathWithin(root, resolved) {
		return plantedLink{}, false
	}

	return plantedLink{path: path, visits: uniqueVisits(walk.visits)}, true
}

// plantedLinksInside fails when a planted link now resolves outside
// OutputDir. A link written at path only moves the links whose resolution
// looked that path up, so only those are walked again; path == "" walks them
// all, for SquashRoot, which changes many names at once.
//
// A link this extract removed is skipped. One it rewrote was checked as text
// and is walked with the new text. Text that no longer walks (a cycle, the
// hop budget) resolves nowhere and is not an escape, but the lookups it made
// are still what a later write has to be matched against. New lookups are
// recorded only when every link passes: on failure the caller puts the
// replaced entry back, and the old lookups describe that layout.
func (x *XFile) plantedLinksInside(path string) error {
	if len(x.planted) == 0 {
		return nil
	}

	if path != "" {
		path = filepath.Join(resolveExisting(filepath.Dir(path)), filepath.Base(path))
	}

	root := resolveExisting(x.OutputDir)
	visits := map[int][]string{}

	for idx := range x.planted {
		link := &x.planted[idx]
		if path != "" && !link.visited(path) {
			continue
		}

		target, err := os.Readlink(link.path)
		if err != nil {
			continue
		}

		walk := &linkWalk{onDisk: x.onDisk}

		resolved, err := walkLinkTarget(link.path, target, walk)
		if err == nil && !pathWithin(root, resolved) {
			return fmt.Errorf("%s: %w: %s -> %s now resolves outside the output folder",
				x.FilePath, ErrInvalidPath, link.path, target)
		}

		visits[idx] = uniqueVisits(walk.visits)
	}

	for idx, seen := range visits {
		x.planted[idx].visits = seen
	}

	return nil
}

func (x *XFile) createHardLink(path, linkName string) error {
	linkWrite.Lock()
	defer linkWrite.Unlock()

	return x.createHardLinkLocked(path, linkName)
}

func (x *XFile) createHardLinkLocked(path, linkName string) error {
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

	old, err := x.replaceEntry(path)
	if err != nil {
		return err
	}

	err = os.Link(target, path)
	if err == nil {
		return x.finishHardLink(path, old)
	}

	linkErr := err

	rel, relErr := filepath.Rel(filepath.Dir(path), target)
	if relErr != nil {
		old.restore()

		return fmt.Errorf("%s: creating hard link: %w: %s => %s", x.FilePath, linkErr, path, target)
	}

	// Fall back to a relative symlink when hard links are unavailable
	// (e.g. target not extracted yet, or the filesystem does not support them).
	x.Debugf("Hard link failed (%v); falling back to symlink: %s -> %s", linkErr, path, rel)

	err = x.ensureLinkWithinOutput(path, rel)
	if err != nil {
		old.restore()

		return err
	}

	return x.placeSymlink(path, rel, old)
}

// finishHardLink validates a hard link whose target was itself a symlink.
// Linux and Windows link the symlink inode, so path is now a second symlink
// with text that was never checked from this directory.
func (x *XFile) finishHardLink(path string, old replacedEntry) error {
	info, err := os.Lstat(path)
	if err != nil {
		old.restore()

		return fmt.Errorf("%s: lstat hard link: %w", x.FilePath, err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err == nil {
			err = x.ensureLinkWithinOutput(path, target)
		}

		if err != nil {
			old.restore()

			return err
		}
	}

	return x.finishLink(path, old)
}
