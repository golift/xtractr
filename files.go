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
	// Suffix brands cross-device copy siblings as a known extra extension
	// (e.g. movie.mkv.xtractr_partial). Empty uses DefaultSuffix.
	Suffix string
	// (RAR/7z) Archive password. Blank for none. Gets prepended to Passwords, below.
	Password string
	// (RAR/7z) Archive passwords (to try multiple).
	Passwords []string
	// FileWorkers controls how many files within a single archive are extracted
	// concurrently. Only effective for random-access formats (ZIP, 7z).
	// Streaming formats ignore this. 0 or 1 = sequential (current behavior).
	// Total concurrent I/O when using the queue = Config.Parallel * FileWorkers.
	FileWorkers int
	// MaxBytes is the maximum uncompressed bytes written for this archive.
	// 0 means unlimited.
	MaxBytes uint64
	// MaxFiles is the maximum files, directories, and symlinks created for this
	// archive. 0 means unlimited.
	MaxFiles int
	// MaxRatio is the maximum bytesWritten / archiveFileSize. 0 means unlimited.
	MaxRatio float64
	// AllowSymlinks allows FilePath to be a symbolic link to an archive.
	AllowSymlinks bool
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
	// refused collects files not moved into place during Extract; it is
	// copied into Response.Refused by processArchive.
	refused []RefusedFile
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
	// MaxArchives stops the walk after this many archives are found. 0 is unlimited.
	MaxArchives int
	// AllowSymlinks includes symlink-named archive files in the listing.
	// Default false. Symlink directories found during the walk are never
	// followed; a symlink passed as Path is (operator-chosen search root).
	// The extras/recursion pass always ignores this and never lists archive-member links.
	AllowSymlinks bool
	// Accept, if set, is called when Find is about to add an archive.
	// Return false to skip it; skipped paths do not count toward MaxArchives.
	Accept func(ArchiveCandidate) bool
}

// ArchiveCandidate is passed to Filter.Accept when Find is about to list a path.
type ArchiveCandidate struct {
	// Path is the archive file being considered.
	Path string
	// Info is the Lstat of Path. May be nil if the path could not be stated.
	Info os.FileInfo
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
func FindCompressedFiles(filter Filter) ArchiveList { //nolint:gocritic // public API; Filter is a value.
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
		if skipSymlinkArchivePath(filter, path) {
			return nil
		}

		if linkInfo, _ := os.Lstat(path); !acceptArchive(filter, path, linkInfo) {
			return nil
		}

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

// RAR multipart name matchers. Compiled once; reused for every directory in a Find walk.
// `$` already anchors the end, so a leading `.*` is unnecessary.
var (
	rarHasParts = regexp.MustCompile(`\.part\d+\.rar$`)
	rarPartOne  = regexp.MustCompile(`\.part0*1\.rar$`)
)

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles(path string, filter *Filter, fileList []os.FileInfo, depth int) ArchiveList { //nolint:cyclop
	files := ArchiveList{}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case archivesAtCap(files, filter):
			return files
		case file.Mode()&os.ModeSymlink != 0 &&
			(!filter.AllowSymlinks || symlinkTargetsDir(path, file.Name())):
			continue // skip symlink archives unless allowed; never walk symlink dirs.
		case !file.IsDir() &&
			(filter.ExcludeSuffix.Has(lowerName) || depth < filter.MinDepth):
			continue // file suffix is excluded or we are not deep enough.
		case lowerName == "" || lowerName[0] == '.':
			continue // ignore empty names and dot files/folders.
		case file.IsDir(): // Recurse.
			maps.Copy(files, findCompressedFiles(filepath.Join(path, file.Name()), filter, depth+1))
		case strings.HasSuffix(lowerName, ".rar"):
			// Some archives are named poorly. Only return part01 or part001, not all.
			if !rarHasParts.MatchString(lowerName) || rarPartOne.MatchString(lowerName) {
				addArchive(files, path, file, filter)
			}
		case strings.HasSuffix(lowerName, ".r00") && !CheckR00ForRarFile(fileList, lowerName):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			addArchive(files, path, file, filter)
		case !strings.HasSuffix(lowerName, ".r00") && IsArchiveFile(lowerName):
			addArchive(files, path, file, filter)
		}
	}

	return files
}

func addArchive(files ArchiveList, dir string, file os.FileInfo, filter *Filter) {
	path := filepath.Join(dir, file.Name())
	if acceptArchive(filter, path, file) {
		files[dir] = append(files[dir], path)
	}
}

func acceptArchive(filter *Filter, path string, info os.FileInfo) bool {
	return filter == nil || filter.Accept == nil || filter.Accept(ArchiveCandidate{Path: path, Info: info})
}

func archivesAtCap(files ArchiveList, filter *Filter) bool {
	return filter != nil && filter.MaxArchives > 0 && files.Count() >= filter.MaxArchives
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
	err = refuseSymlinkArchiveUnlessAllowed(xFile)
	if err != nil {
		return 0, nil, nil, wrapSymlinkArchiveError(xFile, err)
	}

	sName := strings.ToLower(xFile.FilePath)
	xFile.bindMoveFiles()

	var extensionType string // archive type from matched extension, for error reporting when extraction fails

	for _, ext := range extension2function {
		if !strings.HasSuffix(sName, ext.Ext) {
			continue
		}

		size, filesList, archiveList, err = ext.Fn(xFile)
		if err == nil {
			return size, filesList, archiveList, nil
		}

		// A resource cap aborts the extraction; do not re-extract via signature
		// detection, which would reset the counters and could even report success.
		if isLimitError(err) {
			return size, filesList, archiveList, err
		}

		extensionType = ext.Type // preserve for error reporting before fallback
		// Extension matched but extraction failed; try signature detection as fallback.
		break
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
func (x *XFile) bindMoveFiles() { //nolint:funcorder // kept next to MoveFiles
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

func skipSymlinkArchivePath(filter *Filter, path string) bool {
	if filter != nil && filter.AllowSymlinks {
		return false
	}

	linkInfo, linkErr := os.Lstat(path)

	return linkErr != nil || linkInfo.Mode()&os.ModeSymlink != 0
}

func wrapSymlinkArchiveError(xFile *XFile, err error) error {
	if xFile == nil {
		return err
	}

	return NewExtractError(err, xFile.FilePath, xFile.OutputDir, 0, "")
}

func refuseSymlinkArchiveUnlessAllowed(xFile *XFile) error {
	if xFile == nil || xFile.AllowSymlinks {
		return nil
	}

	return refuseSymlinkArchive(xFile.FilePath)
}

func symlinkTargetsDir(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err != nil || info.IsDir()
}

func refuseSymlinkArchive(path string) error {
	if path == "" {
		return nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("lstat archive: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %s", ErrArchiveSymlink, path)
	}

	return nil
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
