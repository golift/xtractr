package xtractr

/* Code to move, rename, and delete files. */

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// GetFileList returns all the files in a path or paths.
// This is non-recursive and only returns files _in_ the base paths provided.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) GetFileList(paths ...string) ([]string, error) {
	return listFiles(paths...)
}

func listFiles(paths ...string) ([]string, error) {
	files := []string{}

	for _, path := range paths {
		stat, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("stat: %w", err)
		}

		if !stat.IsDir() {
			files = append(files, path)
			continue
		}

		fileList, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("reading path %s: %w", path, err)
		}

		for _, file := range fileList {
			files = append(files, filepath.Join(path, file.Name()))
		}
	}

	return files, nil
}

// Difference returns all the strings that are in slice2 but not in slice1.
// Used to find new files in a file list from a path. ie. those we extracted.
// This is a helper method and only exposed for convenience. You do not have to call this.
func Difference(slice1, slice2 []string) []string {
	seen := make(map[string]struct{}, len(slice1))
	for _, item := range slice1 {
		seen[item] = struct{}{}
	}

	diff := make([]string, 0, len(slice2))

	for _, item := range slice2 {
		if _, found := seen[item]; !found {
			diff = append(diff, item)
		}
	}

	return diff
}

// Renamed is the result of RenameFiles.
type Renamed struct {
	// NewFiles is the list of paths that were moved into place.
	NewFiles []string
	// Refused lists files that were not moved because the destination was occupied.
	Refused []RefusedFile
	// Dest is the directory files were moved into (toPath, after any archive
	// extension strip). It is "" when the move did not complete (err != nil).
	// It is set whenever the move completes without error, even when NewFiles
	// is empty (e.g. a squash where files were already in place).
	Dest string
}

// RefusedFile describes an extracted file that was not moved into place
// because the destination path was already occupied and overwrite was false.
// The occupying file was left untouched. The extracted copy is deleted when
// the overall move succeeds, but may remain if another move fails.
type RefusedFile struct {
	// Src is the extracted copy's path in the temporary folder.
	Src string
	// Dest is the occupied destination path that was kept.
	Dest string
}

// MoveFiles relocates files then removes the folder they were in.
// Returns the new file paths. Occupied destinations are skipped when overwrite
// is false; use RenameFiles to learn which names were refused.
// This is a helper method and only exposed for convenience. You do not have to call this.
//
// Deprecated: Use RenameFiles instead.
func (x *Xtractr) MoveFiles(fromPath, toPath string, overwrite bool) ([]string, error) {
	renamed, err := x.RenameFiles(fromPath, toPath, overwrite)
	return renamed.NewFiles, err
}

// RenameFiles relocates files then removes the folder they were in.
// Unlike MoveFiles, the result includes destinations that were already occupied.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) RenameFiles(fromPath, toPath string, overwrite bool) (Renamed, error) {
	return moveFiles(x.config, x.config.DirMode, fromPath, toPath, overwrite, x.config.Suffix)
}

// bindMoveFiles wires ExtractFile to this job's logger and DirMode.
// The old parseConfig(&Config{Logger: x.log}) path allocated a throwaway
// Xtractr (and its done channel) per extract. A pre-set stub is left in
// place so tests can inject one.
func (x *XFile) bindMoveFiles() {
	if x.moveFiles != nil {
		return
	}

	x.moveFiles = func(fromPath, toPath string, overwrite bool) ([]string, error) {
		renamed, err := moveFiles(x.log, x.DirMode, fromPath, toPath, overwrite, x.Suffix)
		x.refused = append(x.refused, renamed.Refused...)

		return renamed.NewFiles, err
	}
}

func moveFiles( //nolint:cyclop,funlen
	log Logger,
	dirMode os.FileMode,
	fromPath, toPath string,
	overwrite bool,
	suffix string,
) (Renamed, error) {
	if log == nil {
		log = NoLogger()
	}

	if dirMode == 0 {
		dirMode = DefaultDirMode
	}

	var (
		newFiles = []string{}
		refused  []RefusedFile
		keepErr  error
	)

	files, err := listFiles(fromPath)
	if err != nil {
		return Renamed{}, err
	}

	// If the "to path" is an existing archive file, remove the suffix to make a directory.
	_, err = os.Stat(toPath)
	if err == nil && IsArchiveFile(toPath) {
		toPath = strings.TrimSuffix(toPath, filepath.Ext(toPath))
	}

	log.Debugf("Moving files: %v (%d files) -> %v", fromPath, len(files), toPath)

	err = os.MkdirAll(toPath, dirMode)
	if err != nil {
		return Renamed{}, fmt.Errorf("making final dir: %w", err)
	}

	// Scavenge once per move; in-flight siblings are registered and skipped,
	// so parallel extractors sharing this destination are unaffected.
	scavengePartials(toPath, suffix)

	for _, file := range files {
		newFile := filepath.Join(toPath, filepath.Base(file))
		if filepath.Clean(file) == filepath.Clean(newFile) {
			// Already at the destination (squash of a lone top-level file).
			newFiles = append(newFiles, newFile)
			continue
		}

		_, err = os.Lstat(newFile)
		exists := err == nil

		if err != nil && !os.IsNotExist(err) {
			// Permission or I/O error is not an occupied destination.
			keepErr = err
			log.Printf("Error: Checking Temp File Destination: %v to %v: %v", file, newFile, err)

			continue
		}

		if exists && !overwrite {
			log.Printf("Error: Renaming Temp File: %v to %v: (refusing to overwrite existing file)", file, newFile)
			refused = append(refused, RefusedFile{Src: file, Dest: newFile})
			// keep trying.
			continue
		}

		switch err = renameFile(file, newFile, suffix); {
		case err != nil:
			keepErr = err
			log.Printf("Error: Renaming Temp File: %v to %v: %v", file, newFile, err)
		case exists:
			newFiles = append(newFiles, newFile)
			log.Debugf("Renamed Temp File: %v -> %v (overwrote existing file)", file, newFile)
		default:
			newFiles = append(newFiles, newFile)
			log.Debugf("Renamed Temp File: %v -> %v", file, newFile)
		}
	}

	// Only remove the temp source when every file moved. On a move or Lstat
	// error (keepErr != nil) the destination holds a partial result, so the
	// source must survive for recovery. Refusals are not errors; those files
	// stay in the temp dir but the destination is otherwise complete.
	if keepErr == nil {
		info, statErr := os.Stat(fromPath)
		if statErr == nil && info.IsDir() {
			deleteFiles(log, fromPath)
		}
	}

	// Since this is the last step, we tried to rename all the files, bubble the
	// os.Rename error up, so it gets flagged as failed. It may have worked, but
	// it should get attention.
	//
	// Dest is set whenever the move step completed (keepErr == nil): the
	// destination is known even if NewFiles is empty (e.g. the lone top-level
	// file was already in place, so nothing needed renaming).
	dest := ""
	if keepErr == nil {
		dest = toPath
	}

	return Renamed{NewFiles: newFiles, Refused: refused, Dest: dest}, keepErr
}

// DeleteFiles obliterates things and logs. Use with caution.
func (x *Xtractr) DeleteFiles(files ...string) {
	deleteFiles(x.config, files...)
}

func deleteFiles(log Logger, files ...string) {
	if log == nil {
		log = NoLogger()
	}

	for _, file := range files {
		err := os.RemoveAll(file)
		if err != nil {
			log.Printf("Error: Deleting %v: %v", file, err)

			continue
		}

		log.Printf("Deleted (recursively): %s", file)
	}
}

// Rename is an attempt to deal with "invalid cross link device" on weird file systems.
func (x *Xtractr) Rename(oldpath, newpath string) error {
	return renameFile(oldpath, newpath, x.config.Suffix)
}

const (
	partialTail = "_partial"
	linkTail    = "_link"
)

func defaultSuffix(suffix string) string {
	if suffix == "" {
		return DefaultSuffix
	}

	return suffix
}

// copySiblingExt turns Config.Suffix ("_xtractr") into a dotted extra
// extension: ".xtractr_partial" so leftovers look like movie.mkv.xtractr_partial.
func copySiblingExt(suffix, tail string) string {
	return "." + strings.Trim(defaultSuffix(suffix), "._") + tail
}

func renameFile(oldpath, newpath, suffix string) error {
	origErr := os.Rename(oldpath, newpath)
	if origErr == nil {
		return nil
	}

	origErr = fmt.Errorf("os.Rename(): %w", origErr)

	err := copyMove(oldpath, newpath, suffix)
	if err != nil {
		return &ExtractError{Errs: []error{origErr, err}}
	}

	return nil
}

// copyMove is the EXDEV fallback for renameFile. It is a separate function so
// tests can exercise it without forcing a cross-device rename.
func copyMove(oldpath, newpath, suffix string) error {
	oldInfo, err := os.Lstat(oldpath)
	if err != nil {
		return fmt.Errorf("os.Lstat(): %w", err)
	}

	if oldInfo.Mode()&os.ModeSymlink != 0 {
		return moveSymlink(oldpath, newpath, suffix)
	}

	// A directory source never makes sense here; fail before scavenging or
	// touching the destination so the caller doesn't delete it.
	if !oldInfo.Mode().IsRegular() {
		return fmt.Errorf("cannot move non-regular file %s: %w", oldpath, errExtractNotRegular)
	}

	return copyMoveFile(oldpath, newpath, suffix, oldInfo)
}

func copyMoveFile(oldpath, newpath, suffix string, oldInfo os.FileInfo) error {
	oldFile, err := os.Open(oldpath)
	if err != nil {
		return fmt.Errorf("os.Open(): %w", err)
	}

	defer oldFile.Close() // also closed explicitly before the delete below.

	// createSibling exclusively creates the partial (no-follow), so a name
	// raced in by another process is never truncated.
	newFile, pathUsed, release, err := createSibling(newpath, suffix, oldInfo.Mode())
	if err != nil {
		return err
	}

	defer release() // after the rename (or error cleanup) below.

	_, err = io.Copy(newFile, oldFile)
	closeErr := newFile.Close()

	if err != nil {
		_ = os.Remove(pathUsed)

		return fmt.Errorf("io.Copy(): %w", err)
	}

	if closeErr != nil {
		_ = os.Remove(pathUsed)

		return fmt.Errorf("closing dest: %w", closeErr)
	}

	_ = os.Chtimes(pathUsed, oldInfo.ModTime(), oldInfo.ModTime())

	err = renameOver(pathUsed, newpath)
	if err != nil {
		_ = os.Remove(pathUsed)

		return err
	}

	_ = oldFile.Close() // Needs to be closed before delete.
	_ = os.Remove(oldpath)

	return nil
}

func moveSymlink(oldpath, newpath, suffix string) error {
	target, err := os.Readlink(oldpath)
	if err != nil {
		return fmt.Errorf("os.Readlink(): %w", err)
	}

	// Reserve an exclusive sibling name, then point the new link at it.
	tmp, release, err := createSymlinkSibling(newpath, suffix, linkTail, target)
	if err != nil {
		return err
	}

	defer release() // after the rename (or error cleanup) below.

	// rename-over replaces newpath atomically; if it was a symlink, the link is
	// severed (never followed) rather than written through.
	err = renameOver(tmp, newpath)
	if err != nil {
		_ = os.Remove(tmp)

		return err
	}

	_ = os.Remove(oldpath)

	return nil
}

// renameOver replaces newpath with oldpath via os.Rename. On Windows a rename
// cannot replace a directory (junction or directory symlink) with a file, so
// when the rename fails and newpath is itself a symlink (never followed), the
// link is unlinked and the rename retried. A real directory is never removed.
//
// When newpath exceeds NAME_MAX the rename fails with ENAMETOOLONG; retry
// against TruncatePathForFS so the cross-device fallback matches the extract
// writers (openExtractFile), which already truncate the final destination.
func renameOver(oldpath, newpath string) error {
	err := os.Rename(oldpath, newpath)
	if err == nil {
		return nil
	}

	if IsErrNameTooLong(err) {
		short, truncErr := TruncatePathForFS(newpath)
		if truncErr != nil {
			return truncErr
		}

		if short != newpath {
			return renameOver(oldpath, short)
		}
	}

	info, lerr := os.Lstat(newpath)
	if lerr != nil || info.Mode()&os.ModeSymlink == 0 {
		return fmt.Errorf("os.Rename(): %w", err)
	}

	// newpath is a symlink (possibly a directory symlink on Windows). Remove the
	// link itself, then rename. We never touch the link's target.
	rmErr := os.Remove(newpath)
	if rmErr != nil {
		return fmt.Errorf("os.Rename(): %w (removing symlink: %w)", err, rmErr)
	}

	retryErr := os.Rename(oldpath, newpath)
	if retryErr != nil {
		return fmt.Errorf("os.Rename(): %w", retryErr)
	}

	return nil
}

// siblingStem is the shared basename for a copy sibling: the dest basename
// truncated so the full sibling name fits NAME_MAX.
func siblingStem(dest, suffix, tail string) (dir, stem, fullTail string) {
	fullTail = copySiblingExt(suffix, tail)
	dir = filepath.Dir(dest)
	base := filepath.Base(dest)
	stem = truncateToBytes(base, max(nameMax-len(fullTail), 1))

	return dir, stem, fullTail
}

// siblingCandidate returns the sibling path for a given attempt (0 = bare).
func siblingCandidate(dir, stem, fullTail string, attempt int) string {
	if attempt == 0 {
		return filepath.Join(dir, stem+fullTail)
	}

	postfix := "." + strconv.Itoa(attempt)
	newStem := truncateToBytes(stem, max(nameMax-len(fullTail)-len(postfix), 1))

	return filepath.Join(dir, newStem+fullTail+postfix)
}

// activeSiblings tracks in-flight copy sibling paths so a concurrent
// scavengePartials skips them instead of unlinking a partial mid-copy.
// Registering (in createSibling/createSymlinkSibling) and the scavenger's
// check-and-remove both hold this lock, so no window exists between creating
// a sibling and marking it active. This is what replaces a move-wide lock and
// lets parallel extractors share a destination directory.
//
//nolint:gochecknoglobals // process-local registry shared by every extractor in it.
var activeSiblings = struct {
	sync.Mutex

	set map[string]struct{}
}{set: make(map[string]struct{})}

// claimSibling marks path active; the returned release func marks it inactive
// and must be called only after path has been renamed away or removed.
func claimSibling(path string) func() {
	return func() {
		activeSiblings.Lock()
		delete(activeSiblings.set, path)
		activeSiblings.Unlock()
	}
}

// removeInactiveSibling removes path unless another move is still copying it.
func removeInactiveSibling(path string) {
	activeSiblings.Lock()
	if _, active := activeSiblings.set[path]; !active {
		_ = os.Remove(path)
	}
	activeSiblings.Unlock()
}

// createSibling exclusively creates the <dest>.<brand>_partial copy sibling,
// registers it as active, and returns the open, no-follow file, its path and
// a release func. On EEXIST it tries the next numbered candidate, so a name
// raced in by another process is never opened/truncated.
func createSibling(dest, suffix string, mode os.FileMode) (*os.File, string, func(), error) {
	dir, stem, fullTail := siblingStem(dest, suffix, partialTail)

	// The create+register pair must be atomic with the scavenger's
	// check-and-remove; creating is a metadata-only syscall, so holding the
	// registry lock across the (few) attempts here is cheap.
	activeSiblings.Lock()
	defer activeSiblings.Unlock()

	for attempt := range 1001 {
		tryPath := siblingCandidate(dir, stem, fullTail, attempt)

		file, err := openFileNoFollow(tryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err == nil {
			activeSiblings.set[tryPath] = struct{}{}

			return file, tryPath, claimSibling(tryPath), nil
		}

		// Name taken (or a symlink sits there); try the next candidate.
		if errors.Is(err, os.ErrExist) || errors.Is(err, errExtractSymlink) {
			continue
		}

		return nil, "", nil, err
	}

	return nil, "", nil, ErrNameTooLong
}

// createSymlinkSibling creates a symlink named as a sibling of dest, pointing
// at target, using the same collision-free numbered naming and active
// registration as createSibling. os.Symlink is itself exclusive (fails if the
// name exists), so no Lstat race.
func createSymlinkSibling(dest, suffix, tail, target string) (string, func(), error) {
	dir, stem, fullTail := siblingStem(dest, suffix, tail)

	activeSiblings.Lock()
	defer activeSiblings.Unlock()

	for attempt := range 1001 {
		tryPath := siblingCandidate(dir, stem, fullTail, attempt)

		err := os.Symlink(target, tryPath)
		if err == nil {
			activeSiblings.set[tryPath] = struct{}{}

			return tryPath, claimSibling(tryPath), nil
		}

		if errors.Is(err, os.ErrExist) {
			continue
		}

		return "", nil, fmt.Errorf("os.Symlink(): %w", err)
	}

	return "", nil, ErrNameTooLong
}

// scavengePartials makes one pass over dir and removes leftover copy
// siblings: files named <name><tail> or <name><tail>.<N>, where tail is the
// branded partial or link tail (e.g. .xtractr_partial) and N is numeric.
// The brand marks these as xtractr-created; lookalikes such as
// movie.mkv.xtractr_partial.backup are left alone, and registered in-flight
// siblings of parallel moves are skipped.
func scavengePartials(dir, suffix string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if isCopySibling(entry.Name(), suffix) {
			removeInactiveSibling(filepath.Join(dir, entry.Name()))
		}
	}
}

// isCopySibling reports whether name is an allocator-generated copy sibling:
// <stem><tail> or <stem><tail>.<N> with a non-empty stem and numeric N.
// Matching anchors on the tail, so truncated-stem numbered names match too.
func isCopySibling(name, suffix string) bool {
	for _, tail := range []string{partialTail, linkTail} {
		fullTail := copySiblingExt(suffix, tail)

		if stem, ok := strings.CutSuffix(name, fullTail); ok && stem != "" {
			return true
		}

		dot := strings.LastIndex(name, ".")
		if dot <= 0 || !isDigits(name[dot+1:]) {
			continue
		}

		if stem, ok := strings.CutSuffix(name[:dot], fullTail); ok && stem != "" {
			return true
		}
	}

	return false
}

func isDigits(str string) bool {
	for _, r := range str {
		if r < '0' || r > '9' {
			return false
		}
	}

	return str != ""
}
