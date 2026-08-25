package xtractr

/* Code to find, write, move and delete files. */

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// ArchiveList is the value returned when searching for compressed files.
// The map is directory to list of archives in that directory.
type ArchiveList map[string][]string

type archive struct {
	Type string
	// Ext is passed to strings.HasSuffix.
	Ext string
	// Fn is the extraction function for this extension.
	Fn Interface
}

// Interface is a common interface for extracting compressed or non-compressed files or archives.
type Interface func(x *XFile) (size uint64, filesList, archiveList []string, err error)

// https://github.com/golift/xtractr/issues/44
//
// This list of archive types is used in a few places as extension lists.
//
//nolint:gochecknoglobals
var extension2function = []archive{
	{Type: "tar.bzip2", Ext: ".tar.bz2", Fn: ChngInt(ExtractTarBzip)},
	{Type: "cpio.gzip", Ext: ".cpio.gz", Fn: ChngInt(ExtractCPIOGzip)},
	{Type: "tar.gzip", Ext: ".tar.gz", Fn: ChngInt(ExtractTarGzip)},
	{Type: "tar.xz", Ext: ".tar.xz", Fn: ChngInt(ExtractTarXZ)},
	{Type: "tar.lzw", Ext: ".tar.z", Fn: ChngInt(ExtractTarZ)},
	// The ones with double extensions that match a single (below) need to come first.
	{Type: "7zip", Ext: ".7z", Fn: Extract7z},
	{Type: "7zip", Ext: ".7z.001", Fn: Extract7z},
	{Type: "ar", Ext: ".ar", Fn: ChngInt(ExtractAr)},
	{Type: "brotli", Ext: ".br", Fn: ChngInt(ExtractBrotli)},
	{Type: "brotli", Ext: ".brotli", Fn: ChngInt(ExtractBrotli)},
	{Type: "bz2", Ext: ".bz2", Fn: ChngInt(ExtractBzip)},
	{Type: "cpio.gzip", Ext: ".cpgz", Fn: ChngInt(ExtractCPIOGzip)},
	{Type: "cpio", Ext: ".cpio", Fn: ChngInt(ExtractCPIO)},
	{Type: "deb", Ext: ".deb", Fn: ChngInt(ExtractAr)},
	{Type: "gzip", Ext: ".gz", Fn: ChngInt(ExtractGzip)},
	{Type: "gzip", Ext: ".gzip", Fn: ChngInt(ExtractGzip)},
	{Type: "iso", Ext: ".iso", Fn: ChngInt(ExtractISO)},
	{Type: "lz4", Ext: ".lz4", Fn: ChngInt(ExtractLZ4)},
	{Type: "lzma", Ext: ".lz", Fn: ChngInt(ExtractLZMA)},
	{Type: "lzma", Ext: ".lzip", Fn: ChngInt(ExtractLZMA)},
	{Type: "lzma", Ext: ".lzma", Fn: ChngInt(ExtractLZMA)},
	{Type: "lzma2", Ext: ".lzma2", Fn: ChngInt(ExtractLZMA2)},
	{Type: "rar", Ext: ".r00", Fn: ExtractRAR},
	{Type: "rar", Ext: ".rar", Fn: ExtractRAR},
	{Type: "snappy2", Ext: ".s2", Fn: ChngInt(ExtractS2)},
	{Type: "rpm", Ext: ".rpm", Fn: ChngInt(ExtractRPM)},
	{Type: "snappy", Ext: ".snappy", Fn: ChngInt(ExtractSnappy)},
	{Type: "snappy", Ext: ".sz", Fn: ChngInt(ExtractSnappy)},
	{Type: "tar", Ext: ".tar", Fn: ChngInt(ExtractTar)},
	{Type: "tar.bzip2", Ext: ".tbz", Fn: ChngInt(ExtractTarBzip)},
	{Type: "tar.bzip2", Ext: ".tbz2", Fn: ChngInt(ExtractTarBzip)},
	{Type: "tar.gzip", Ext: ".tgz", Fn: ChngInt(ExtractTarGzip)},
	{Type: "tar.lzma", Ext: ".tlz", Fn: ChngInt(ExtractTarLzip)},
	{Type: "tar.xz", Ext: ".txz", Fn: ChngInt(ExtractTarXZ)},
	{Type: "tar.lzw", Ext: ".tz", Fn: ChngInt(ExtractTarZ)},
	{Type: "xz", Ext: ".xz", Fn: ChngInt(ExtractXZ)},
	{Type: "lzw", Ext: ".z", Fn: ChngInt(ExtractLZW)}, // everything is lowercase...
	{Type: "zip", Ext: ".zip", Fn: ChngInt(ExtractZIP)},
	{Type: "zlib", Ext: ".zlib", Fn: ChngInt(ExtractZlib)},
	{Type: "zstandard", Ext: ".zst", Fn: ChngInt(ExtractZstandard)},
	{Type: "zstandard", Ext: ".zstd", Fn: ChngInt(ExtractZstandard)},
	{Type: "zlib", Ext: ".zz", Fn: ChngInt(ExtractZlib)},
	{Type: "flac", Ext: ".cue.txt", Fn: ExtractCUE},
	{Type: "flac", Ext: ".cue", Fn: ExtractCUE},
}

// ChngInt converts the smaller return interface into an ExtractInterface.
// Functions with multi-part archive files return four values. Other functions return only 3.
// This ChngInt function makes both interfaces compatible.
func ChngInt(smallFn func(*XFile) (uint64, []string, error)) Interface {
	return func(xFile *XFile) (uint64, []string, []string, error) {
		size, files, err := smallFn(xFile)
		return size, files, []string{xFile.FilePath}, err
	}
}

// dispatchWorkers runs work for each entry using a bounded worker pool.
// Dispatch stops when a worker reports an error, in-flight entries finish,
// and the first error encountered is returned. Used by the random-access
// extractors (ZIP, 7z) when XFile.FileWorkers > 1.
func dispatchWorkers[T any](count int, entries []T, work func(T) error) error {
	var (
		waitGroup sync.WaitGroup
		firstErr  atomic.Pointer[error]
		semaphore = make(chan struct{}, count)
	)

	for idx := range entries {
		if firstErr.Load() != nil {
			break
		}

		semaphore <- struct{}{} // acquire worker slot

		waitGroup.Go(func() {
			defer func() { <-semaphore }() // release worker slot

			err := work(entries[idx])
			if err != nil {
				firstErr.CompareAndSwap(nil, &err)
			}
		})
	}

	waitGroup.Wait()

	if err := firstErr.Load(); err != nil {
		return *err
	}

	return nil
}

// SupportedExtensions returns a slice of file extensions this library recognizes.
func SupportedExtensions() []string {
	exts := make([]string, len(extension2function))

	for idx, ext := range extension2function {
		exts[idx] = ext.Ext
	}

	return exts
}

// XFile defines the data needed to extract an archive.
type XFile struct {
	// Path to archive being extracted.
	FilePath string
	// Folder to extract archive into.
	OutputDir string
	// Write files with this mode.
	FileMode os.FileMode
	// Write folders with this mode.
	DirMode os.FileMode
	// (RAR/7z) Archive password. Blank for none. Gets prepended to Passwords, below.
	Password string
	// (RAR/7z) Archive passwords (to try multiple).
	Passwords []string
	// FileWorkers controls how many files within a single archive are extracted
	// concurrently. Only effective for random-access formats (ZIP, 7z).
	// Streaming formats ignore this. 0 or 1 = sequential (current behavior).
	// Total concurrent I/O when using the queue = Config.Parallel * FileWorkers.
	FileWorkers int
	// Progress is called periodically during file extraction.
	// Contains info about the progress of the extraction.
	// This is not called if an Updates channel is also provided.
	Progress func(Progress)
	// If an Updates channel is provided, all Progress updates are sent to it.
	// Contains info about the progress of the extraction.
	Updates chan Progress
	// If the archive only has one directory in the root, then setting
	// this true will cause the extracted content to be moved into the
	// output folder, and the root folder in the archive to be removed.
	SquashRoot bool
	// SkipOnRecursion, if set by an extractor, lists paths that were copied into
	// the output (e.g. a CUE sheet) and must not be re-extracted when recursing.
	SkipOnRecursion []string
	// Logger allows printing debug messages.
	log       Logger
	moveFiles func(fromPath, toPath string, overwrite bool) ([]string, error)
	prog      *progressTracker
}

// Filter is the input to find compressed files.
type Filter struct {
	// This is the path to search in for archives.
	Path string
	// Any files with this suffix are ignored. ie. ".7z" or ".iso"
	// Use the AllExcept func to create an inclusion list instead.
	ExcludeSuffix Exclude
	// Count of folder depth allowed when finding archives. 1 = root
	MaxDepth int
	// Only find archives this many child-folders deep. 0 and 1 are equal.
	MinDepth int
}

// Exclude represents an exclusion list.
type Exclude []string

// Debugf calls the debug method on the logger if it's not nil.
func (x *XFile) Debugf(format string, v ...any) {
	if x.log != nil {
		x.log.Debugf(format, v...)
	}
}

// Printf calls the print method on the logger if it's not nil.
func (x *XFile) Printf(format string, v ...any) {
	if x.log != nil {
		x.log.Printf(format, v...)
	}
}

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
	diff := []string{}

	for _, s2p := range slice2 {
		var found bool

		if slices.Contains(slice1, s2p) {
			found = true
		}

		if !found { // String not found, so it's a new string, add it to the diff.
			diff = append(diff, s2p)
		}
	}

	return diff
}

// Has returns true if the test has an excluded suffix.
func (e Exclude) Has(test string) bool {
	for _, exclude := range e {
		if strings.HasSuffix(test, strings.ToLower(exclude)) {
			return true
		}
	}

	return false
}

// FindCompressedFiles returns all the compressed archive files in a path. This attempts to grab
// only the first file in a multi-part rar or 7zip archive. Sometimes there are multiple archives,
// so if the rar archive does not have "part" followed by a number in the name, then it will be
// considered an independent archive. Some packagers seem to use different naming schemes,
// so this may need to be updated as time progresses. Use the input to Filter to adjust the output.
func FindCompressedFiles(filter Filter) ArchiveList {
	return findCompressedFiles(filter.Path, &filter, 0)
}

func findCompressedFiles(path string, filter *Filter, depth int) ArchiveList {
	if filter.MaxDepth > 0 && filter.MaxDepth < depth {
		return nil
	}

	dir, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer dir.Close()

	info, err := dir.Stat()
	if err != nil {
		return nil // unreadable folder?
	}

	if !info.IsDir() && IsArchiveFile(path) {
		return ArchiveList{path: {path}} // passed in an archive file; send it back out.
	}

	fileList := getFilteredFileList(path, dir)
	if len(fileList) == 0 {
		return nil
	}

	return getCompressedFiles(path, filter, fileList, depth)
}

// getFilteredFileList reads the directory and returns a list of readable files that are not dot files.
func getFilteredFileList(path string, dir *os.File) []os.FileInfo {
	names, _ := dir.Readdirnames(-1)
	fileList := make([]os.FileInfo, 0, len(names))

	for _, name := range names {
		if name == "" || name[0] == '.' {
			continue // skip dot files (including AppleDouble ._* entries)
		}

		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil {
			continue // skip entries we can't stat
		}

		fileList = append(fileList, info)
	}

	return fileList
}

// IsArchiveFile returns true if the provided path has an archive file extension.
// This is not picky about extensions, and will match any that are known as an archive.
// In the future, it may use file magic to figure out if the file is an archive without
// relying on the extension.
func IsArchiveFile(path string) bool {
	path = strings.ToLower(path)

	for _, ext := range extension2function {
		if strings.HasSuffix(path, ext.Ext) {
			return true
		}
	}

	return false
}

// CheckR00ForRarFile scans the file list to determine if a .rar file with the same name as .r00 exists.
// Returns true if the r00 files has an accompanying rar file in the fileList.
func CheckR00ForRarFile(fileList []os.FileInfo, r00file string) bool {
	findFile := strings.TrimSuffix(strings.TrimSuffix(r00file, ".R00"), ".r00") + ".rar"

	for _, file := range fileList {
		if strings.EqualFold(file.Name(), findFile) {
			return true
		}
	}

	return false
}

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles(path string, filter *Filter, fileList []os.FileInfo, depth int) ArchiveList { //nolint:cyclop
	files := ArchiveList{}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case !file.IsDir() &&
			(filter.ExcludeSuffix.Has(lowerName) || depth < filter.MinDepth):
			continue // file suffix is excluded or we are not deep enough.
		case lowerName == "" || lowerName[0] == '.':
			continue // ignore empty names and dot files/folders.
		case file.IsDir(): // Recurse.
			maps.Copy(files, findCompressedFiles(filepath.Join(path, file.Name()), filter, depth+1))
		case strings.HasSuffix(lowerName, ".rar"):
			hasParts := regexp.MustCompile(`.*\.part\d+\.rar$`)
			partOne := regexp.MustCompile(`.*\.part0*1\.rar$`)
			// Some archives are named poorly. Only return part01 or part001, not all.
			if !hasParts.MatchString(lowerName) || partOne.MatchString(lowerName) {
				files[path] = append(files[path], filepath.Join(path, file.Name()))
			}
		case strings.HasSuffix(lowerName, ".r00") && !CheckR00ForRarFile(fileList, lowerName):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			files[path] = append(files[path], filepath.Join(path, file.Name()))
		case !strings.HasSuffix(lowerName, ".r00") && IsArchiveFile(lowerName):
			files[path] = append(files[path], filepath.Join(path, file.Name()))
		}
	}

	return files
}

// normalizeVolumes maps the volume list reported by an archive decoder into
// cleaned, deletable paths. Decoders are inconsistent: some return bare
// basenames (rardecode) while others return relative paths (sevenzip). Absolute
// paths and relative paths with directory components are preserved; bare names
// are resolved next to the entry archive file, where split archive volumes are
// expected to live. No filesystem probing is performed so the resulting cleanup
// paths are deterministic and independent of the process working directory.
// Empty or "." volume entries are dropped so cleanup never targets a directory;
// if no usable volumes remain, the entry file path is returned instead.
func normalizeVolumes(volumes []string, filePath string) []string {
	filePath = filepath.Clean(filePath)

	if len(volumes) == 0 {
		return []string{filePath}
	}

	dir := filepath.Dir(filePath)
	normalized := make([]string, 0, len(volumes))

	for _, volume := range volumes {
		volume = filepath.Clean(volume)
		if volume == "." {
			continue
		}

		if filepath.IsAbs(volume) || volume != filepath.Base(volume) {
			normalized = append(normalized, volume)
			continue
		}

		normalized = append(normalized, filepath.Join(dir, filepath.Base(volume)))
	}

	if len(normalized) == 0 {
		return []string{filePath}
	}

	return normalized
}

// Extract calls the correct procedure for the type of file being extracted.
// Returns size of extracted data, list of extracted files, and/or error.
func (x *XFile) Extract() (size uint64, filesList, archiveList []string, err error) {
	return ExtractFile(x)
}

// ExtractFile calls the correct procedure for the type of file being extracted.
// Returns size of extracted data, list of extracted files, list of archives processed, and/or error.
func ExtractFile(xFile *XFile) (size uint64, filesList, archiveList []string, err error) {
	sName := strings.ToLower(xFile.FilePath)
	xFile.bindMoveFiles()

	var extensionType string // archive type from matched extension, for error reporting when extraction fails

	for _, ext := range extension2function {
		if strings.HasSuffix(sName, ext.Ext) {
			size, filesList, archiveList, err = ext.Fn(xFile)
			if err == nil {
				return size, filesList, archiveList, nil
			}

			extensionType = ext.Type // preserve for error reporting before fallback
			// Extension matched but extraction failed; try signature detection as fallback.
			break
		}
	}

	// Fall back to file signature (magic number) detection.
	if err != nil {
		xFile.Debugf("extension-based extraction failed for %s, falling back to signature detection: %v",
			xFile.FilePath, err)
	} else {
		xFile.Debugf("no extension match for %s, falling back to signature detection", xFile.FilePath)
	}

	extractFn, archiveType, sigErr := detectBySignature(xFile.FilePath)
	if sigErr != nil {
		extErr := &ExtractError{
			FilePath:    xFile.FilePath,
			OutputDir:   xFile.OutputDir,
			ArchiveType: extensionType,
		}
		if err != nil {
			extErr.Errs = append(extErr.Errs, err)
		}

		extErr.Errs = append(extErr.Errs, sigErr)

		return 0, nil, nil, extErr
	}

	size, filesList, archiveList, err = extractFn(xFile)
	if err != nil {
		return size, filesList, archiveList, WrapExtractError(err, xFile, size, archiveType)
	}

	return size, filesList, archiveList, nil
}

// MoveFiles relocates files then removes the folder they were in.
// Returns the new file paths.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) MoveFiles(fromPath, toPath string, overwrite bool) ([]string, error) {
	return moveFiles(x.config, x.config.DirMode, fromPath, toPath, overwrite)
}

// bindMoveFiles wires ExtractFile to this job's logger and DirMode.
// The old parseConfig(&Config{Logger: x.log}) path allocated a throwaway
// Xtractr (and its done channel) per extract. A pre-set stub is left in
// place so tests can inject one.
func (x *XFile) bindMoveFiles() { //nolint:funcorder // kept next to MoveFiles
	if x.moveFiles != nil {
		return
	}

	x.moveFiles = func(fromPath, toPath string, overwrite bool) ([]string, error) {
		return moveFiles(x.log, x.DirMode, fromPath, toPath, overwrite)
	}
}

func moveFiles( //nolint:cyclop,funlen
	log Logger,
	dirMode os.FileMode,
	fromPath, toPath string,
	overwrite bool,
) ([]string, error) {
	if log == nil {
		log = NoLogger()
	}

	if dirMode == 0 {
		dirMode = DefaultDirMode
	}

	var (
		newFiles = []string{}
		keepErr  error
	)

	files, err := listFiles(fromPath)
	if err != nil {
		return nil, err
	}

	// If the "to path" is an existing archive file, remove the suffix to make a directory.
	_, err = os.Stat(toPath)
	if err == nil && IsArchiveFile(toPath) {
		toPath = strings.TrimSuffix(toPath, filepath.Ext(toPath))
	}

	log.Debugf("Moving files: %v (%d files) -> %v", fromPath, len(files), toPath)

	err = os.MkdirAll(toPath, dirMode)
	if err != nil {
		return nil, fmt.Errorf("making final dir: %w", err)
	}

	for _, file := range files {
		newFile := filepath.Join(toPath, filepath.Base(file))
		if filepath.Clean(file) == filepath.Clean(newFile) {
			// Already at the destination (squash of a lone top-level file).
			newFiles = append(newFiles, newFile)
			continue
		}

		_, err = os.Stat(newFile)
		exists := !os.IsNotExist(err)

		if exists && !overwrite {
			log.Printf("Error: Renaming Temp File: %v to %v: (refusing to overwrite existing file)", file, newFile)
			// keep trying.
			continue
		}

		switch err = renameFile(file, newFile); {
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

	info, statErr := os.Stat(fromPath)
	if statErr == nil && info.IsDir() {
		deleteFiles(log, fromPath)
	}

	// Since this is the last step, we tried to rename all the files, bubble the
	// os.Rename error up, so it gets flagged as failed. It may have worked, but
	// it should get attention.
	return newFiles, keepErr
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

// nameMax is the typical filesystem limit for a single path component (POSIX NAME_MAX).
const nameMax = 255

// TruncatePathForFS returns a path that fits within filesystem name limits by
// truncating the last path component (the filename) to nameMax bytes and, if
// that name already exists in the directory, appending ~1, ~2, etc. until an
// available name is found. The extension is preserved; the stem is truncated at
// UTF-8 rune boundaries. Use this when IsErrNameTooLong indicates a path is too long.
//
//nolint:nilerr
func TruncatePathForFS(path string) (string, error) {
	var (
		dir     = filepath.Dir(path)
		ext     = filepath.Ext(path)
		base    = strings.TrimSuffix(filepath.Base(path), ext)
		stem    = truncateToBytes(base, max(nameMax-len(ext), 1))
		tryPath = filepath.Join(dir, stem+ext)
	)

	_, err := os.Lstat(tryPath)
	if err != nil { // path doesn't exist or other error; caller can try to create it
		return tryPath, nil
	}

	for attempt := range 1000 {
		postfix := "~" + strconv.Itoa(attempt+1)
		newStem := truncateToBytes(stem, max(nameMax-len(ext)-len(postfix), 1))
		tryPath = filepath.Join(dir, newStem+postfix+ext)

		_, err = os.Lstat(tryPath)
		if err != nil {
			return tryPath, nil
		}
	}

	return "", ErrNameTooLong
}

// truncateToBytes shortens s to at most maxBytes bytes, on UTF-8 rune boundaries.
// It returns s unchanged if maxBytes is negative or zero to avoid infinite loops or panics.
func truncateToBytes(str string, maxBytes int) string {
	if maxBytes <= 0 || len(str) <= maxBytes {
		if maxBytes <= 0 {
			return ""
		}

		return str
	}

	raw := []byte(str)
	for len(raw) > maxBytes {
		_, size := utf8.DecodeLastRune(raw)
		raw = raw[:len(raw)-size]
	}

	return string(raw)
}

// openFile opens path with the given flags and mode. If the path exceeds
// filesystem name limits, the path is truncated via TruncatePathForFS and
// retried. It returns the opened file and the path that was actually used
// (the original or the truncated path), so the caller can update file.Path
// for later use (e.g. os.Chtimes).
func openFile(path string, flags int, mode os.FileMode) (*os.File, string, error) {
	openFile, err := os.OpenFile(path, flags, mode)
	if err == nil {
		return openFile, path, nil
	}

	if !IsErrNameTooLong(err) {
		return nil, "", fmt.Errorf("os.OpenFile(): %w", err)
	}

	shortPath, truncErr := TruncatePathForFS(path)
	if truncErr != nil {
		return nil, "", truncErr
	}

	openFile, err = os.OpenFile(shortPath, flags, mode)
	if err != nil {
		return nil, "", fmt.Errorf("os.OpenFile(): %w", err)
	}

	return openFile, shortPath, nil
}

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

// Rename is an attempt to deal with "invalid cross link device" on weird file systems.
func (x *Xtractr) Rename(oldpath, newpath string) error {
	return renameFile(oldpath, newpath)
}

func renameFile(oldpath, newpath string) error {
	origErr := os.Rename(oldpath, newpath)
	if origErr == nil {
		return nil
	}

	origErr = fmt.Errorf("os.Rename(): %w", origErr)

	/* Rename failed, try copy. */

	oldFileStat, err := os.Stat(oldpath)
	if err != nil {
		return &ExtractError{Errs: []error{origErr, fmt.Errorf("os.Stat(): %w", err)}}
	}

	oldFile, err := os.Open(oldpath)
	if err != nil {
		return &ExtractError{Errs: []error{origErr, fmt.Errorf("os.Open(): %w", err)}}
	}

	defer oldFile.Close() // also closed explicitly before the delete below.

	newFile, pathUsed, err := openFile(newpath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, oldFileStat.Mode())
	if err != nil {
		return &ExtractError{Errs: []error{origErr, err}}
	}

	_, err = io.Copy(newFile, oldFile)
	closeErr := newFile.Close()

	if err != nil {
		_ = os.Remove(pathUsed)

		return &ExtractError{Errs: []error{origErr, fmt.Errorf("io.Copy(): %w", err)}}
	}

	if closeErr != nil {
		_ = os.Remove(pathUsed)

		return &ExtractError{Errs: []error{origErr, fmt.Errorf("closing dest: %w", closeErr)}}
	}

	// pathUsed may differ from newpath if the name had to be truncated.
	_ = os.Chtimes(pathUsed, oldFileStat.ModTime(), oldFileStat.ModTime())
	// The copy was successful, so now delete the original file
	_ = oldFile.Close() // Needs to be closed before delete.
	_ = os.Remove(oldpath)

	return nil
}

// AllExcept can be used as an input to ExcludeSuffix in a Filter.
// Returns a list of supported extensions minus the ones provided.
// Extensions for like-types such as .rar and .r00 need to both be provided.
// Same for .tar.gz and .tgz variants. Passing .cue also keeps .cue.txt.
func AllExcept(onlyThese ...string) Exclude {
	keep := make([]string, 0, len(onlyThese)+1)
	keep = append(keep, onlyThese...)

	for _, str := range onlyThese {
		if strings.EqualFold(str, ".cue") {
			keep = append(keep, ".cue.txt")
			break
		}
	}

	// Start by excluding everything.
	output := SupportedExtensions()

	// Loop through the extensions we want to keep.
	for _, str := range keep {
		idx := 0
		// Remove each one from the output list.
		for _, ext := range output {
			if !strings.EqualFold(ext, str) {
				output[idx] = ext
				idx++
			}
		}
		// Truncate the output to the size of items kept.
		output = output[:idx]
	}

	return output
}

// Count returns the number of unique archives in the archive list.
func (a ArchiveList) Count() int {
	var count int

	for _, files := range a {
		count += len(files)
	}

	return count
}

// Random returns a random file listing from the archive list.
// If the list only contains one directory, then that is the one returned.
// If the archive list is empty or nil, returns nil.
func (a ArchiveList) Random() []string {
	for _, files := range a {
		return files
	}

	return nil
}

// List returns all of the archives as a string slice.
func (a ArchiveList) List() []string {
	list := make([]string, 0, len(a))

	for _, files := range a {
		list = append(list, files...)
	}

	return list
}

// SetLogger sets the logger interface on an XFile. Useful when you need to debug what it's doing.
func (x *XFile) SetLogger(logger Logger) {
	x.log = logger
}

// cleanup runs after a successful extract.
// The intent it to move files into their final location.
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
	// Check before MkdirAll so we do not create directories (or Chtimes them)
	// through a symlink that already points outside OutputDir.
	if !x.resolvedWithinOutput(path) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, path)
	}

	err := os.MkdirAll(path, x.safeDirMode(mode))
	if err != nil {
		return err //nolint:wrapcheck
	}

	// Recheck after create: MkdirAll follows symlinks, so a race could still
	// land the new folder outside OutputDir.
	if !x.resolvedWithinOutput(path) {
		return fmt.Errorf("%s: %w: %s resolves outside the output folder", x.FilePath, ErrInvalidPath, path)
	}

	_ = os.Chtimes(path, time.Time{}, mtime)

	return nil
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

	progWriter := x.prog.writer(fout)
	if parallel {
		progWriter = x.prog.parallelWriter(fout)
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
// (ELOOP / reparse point) is unlinked and the exclusive create is retried.
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

	if errors.Is(err, errExtractSymlink) || !errors.Is(err, os.ErrExist) {
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
	fout, usedPath, err := openExtractFile(path, mode)
	if err != nil {
		return err
	}

	_, err = fout.Write(data)
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
