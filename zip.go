package xtractr

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding"
)

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (size uint64, filesList []string, err error) {
	zipReader, err := zip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	defer xFile.newProgress(getUncompressedZipSize(zipReader)).done()

	// Detect encoding for non-UTF8 filenames in the archive.
	decoder := detectZipEncoding(xFile, zipReader.File)

	if xFile.FileWorkers > 1 {
		return xFile.extractZIPParallel(zipReader, decoder)
	}

	files := []string{}

	for _, zipFile := range zipReader.File {
		decodedName := decodeZipFilename(zipFile.Name, zipFile.NonUTF8, decoder)

		fSize, wfile, err := xFile.unzipWithName(zipFile, decodedName)
		if err != nil {
			return xFile.prog.Wrote, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, decodedName))
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			wfile, fSize, xFile.prog.Files, xFile.prog.Wrote)
	}

	files, err = xFile.cleanup(files)

	return xFile.prog.Wrote, files, err
}

func getUncompressedZipSize(zipReader *zip.ReadCloser) (total, compressed uint64, count int) {
	for _, zipFile := range zipReader.File {
		total += zipFile.UncompressedSize64
		// compressed += zipFile.CompressedSize64
		count++
	}

	return total, 0, count
}

// zipFileEntry holds a decoded zip file entry for the parallel dispatch pass.
type zipFileEntry struct {
	zipFile     *zip.File
	decodedName string
}

// extractZIPParallel extracts zip files using a bounded worker pool.
// Pass 1 (sequential): create directories and build a list of file entries.
// Pass 2 (parallel): dispatch file writes to workers.
func (x *XFile) extractZIPParallel(
	zipReader *zip.ReadCloser,
	decoder *encoding.Decoder,
) (uint64, []string, error) {
	fileEntries, files, err := x.zipPrepareEntries(zipReader, decoder)
	if err != nil {
		return x.prog.Wrote, files, err
	}

	workerErr := x.zipDispatchWorkers(fileEntries)
	if workerErr != nil {
		return x.prog.Wrote, files, workerErr
	}

	files, err = x.cleanup(files)

	return x.prog.Wrote, files, err
}

// zipPrepareEntries iterates all entries, creates directories, validates paths,
// and returns the list of file entries to extract in parallel.
func (x *XFile) zipPrepareEntries(
	zipReader *zip.ReadCloser,
	decoder *encoding.Decoder,
) ([]zipFileEntry, []string, error) {
	entries := make([]zipFileEntry, 0, len(zipReader.File))
	files := make([]string, 0, len(zipReader.File))

	for _, zipFile := range zipReader.File {
		decodedName := decodeZipFilename(zipFile.Name, zipFile.NonUTF8, decoder)
		cleanPath := x.clean(decodedName)

		if !strings.HasPrefix(cleanPath, x.OutputDir) {
			return nil, files, fmt.Errorf("%s: %s: %w: %s (from: %s)",
				x.FilePath, zipFile.FileInfo().Name(), ErrInvalidPath, cleanPath, decodedName)
		}

		files = append(files, filepath.Join(x.OutputDir, decodedName))

		if zipFile.FileInfo().IsDir() {
			err := x.mkDir(cleanPath, zipFile.Mode(), zipFile.Modified)
			if err != nil {
				return nil, files, fmt.Errorf("%s: making zipFile dir: %w", x.FilePath, err)
			}

			continue
		}

		entries = append(entries, zipFileEntry{zipFile: zipFile, decodedName: decodedName})
	}

	return entries, files, nil
}

// zipDispatchWorkers sends file entries to a bounded worker pool for extraction.
func (x *XFile) zipDispatchWorkers(entries []zipFileEntry) error {
	var (
		waitGroup sync.WaitGroup
		firstErr  error
		errOnce   sync.Once
		semaphore = make(chan struct{}, x.FileWorkers)
	)

	for idx := range entries {
		entry := entries[idx]

		if firstErr != nil {
			break
		}

		semaphore <- struct{}{} // acquire worker slot

		waitGroup.Go(func() {
			defer func() { <-semaphore }() // release worker slot

			err := x.extractZIPEntry(entry)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		})
	}

	waitGroup.Wait()

	return firstErr
}

// extractZIPEntry extracts a single zip file entry (used by parallel workers).
func (x *XFile) extractZIPEntry(entry zipFileEntry) error {
	zFile, err := entry.zipFile.Open()
	if err != nil {
		return fmt.Errorf("%s: zipFile.Open: %w", x.FilePath, err)
	}
	defer zFile.Close()

	fileInfo := &file{
		Path:     x.clean(entry.decodedName),
		Data:     zFile,
		FileMode: entry.zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    entry.zipFile.Modified,
		Atime:    time.Now(),
	}

	_, err = x.writeParallel(fileInfo)
	if err != nil {
		return fmt.Errorf("%s: %w: %s (from: %s)",
			entry.zipFile.FileInfo().Name(), err, fileInfo.Path, entry.decodedName)
	}

	return nil
}

func (x *XFile) unzipWithName(zipFile *zip.File, name string) (uint64, string, error) {
	zFile, err := zipFile.Open()
	if err != nil {
		return 0, name, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	file := &file{
		Path:     x.clean(name),
		Data:     zFile,
		FileMode: zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    zipFile.Modified,
		Atime:    time.Now(),
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		err := fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, file.Path, name)
		return 0, file.Path, err
	}

	if zipFile.FileInfo().IsDir() {
		x.Debugf("Writing archived directory: %s", file.Path)

		err := x.mkDir(file.Path, zipFile.Mode(), zipFile.Modified)
		if err != nil {
			return 0, file.Path, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, file.Path, nil
	}

	x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)", file.Path,
		zipFile.CompressedSize64, zipFile.UncompressedSize64)

	s, err := x.write(file)
	if err != nil {
		return s, file.Path, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, file.Path, name)
	}

	return s, file.Path, nil
}
