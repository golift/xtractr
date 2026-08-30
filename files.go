package xtractr

/* Archive types, XFile, extract dispatch, and shared path-name helpers. */

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

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
		if IsLimitError(err) {
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

// SetLogger sets the logger interface on an XFile. Useful when you need to debug what it's doing.
func (x *XFile) SetLogger(logger Logger) {
	x.log = logger
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
