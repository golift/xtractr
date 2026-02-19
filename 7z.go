package xtractr

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/bodgit/sevenzip"
)

// Extract7z extracts a 7zip archive.
// Volumes: https://github.com/bodgit/sevenzip/issues/54
func Extract7z(xFile *XFile) (size uint64, filesList, archiveList []string, err error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		return extract7z(xFile)
	}

	// Try all the passwords.
	passwords := xFile.Passwords

	if xFile.Password != "" { // If a single password is provided, try it first.
		passwords = append([]string{xFile.Password}, xFile.Passwords...)
	}

	for idx, password := range passwords {
		size, files, archives, err := extract7z(&XFile{
			FilePath:    xFile.FilePath,
			OutputDir:   xFile.OutputDir,
			FileMode:    xFile.FileMode,
			DirMode:     xFile.DirMode,
			Password:    password,
			FileWorkers: xFile.FileWorkers,
		})
		if err != nil && idx == len(passwords)-1 {
			return size, files, archives, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
		} else if err == nil {
			return size, files, archives, nil
		}
	}

	// unreachable code
	return 0, nil, nil, nil
}

func extract7z(xFile *XFile) (uint64, []string, []string, error) {
	sevenZip, err := sevenzip.OpenReaderWithPassword(xFile.FilePath, xFile.Password)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s: os.Open: %w", xFile.FilePath, err)
	}

	defer xFile.newProgress(getUncompressed7zSize(sevenZip)).done() // this closes sevenZip

	sevenZip, err = sevenzip.OpenReaderWithPassword(xFile.FilePath, xFile.Password)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s: os.Open: %w", xFile.FilePath, err)
	}
	defer sevenZip.Close()

	if xFile.FileWorkers > 1 {
		return xFile.extract7zParallel(sevenZip)
	}

	files := []string{}

	for _, zipFile := range sevenZip.File {
		fSize, wfile, err := xFile.un7zip(zipFile)
		if err != nil {
			return xFile.prog.Wrote, files, []string{xFile.FilePath}, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, zipFile.Name))
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			wfile, fSize, xFile.prog.Files, xFile.prog.Wrote)
	}

	files, err = xFile.cleanup(files)

	return xFile.prog.Wrote, files, []string{xFile.FilePath}, err
}

func getUncompressed7zSize(reader *sevenzip.ReadCloser) (total, compressed uint64, count int) {
	defer reader.Close()

	for _, zipFile := range reader.File {
		total += zipFile.UncompressedSize
		// compressed += uint64(zipFile.FileInfo().Size())
		count++
	}

	return total, 0, count
}

// sevenZipEntry holds a 7z file entry for the parallel dispatch pass.
type sevenZipEntry struct {
	sevenZipFile *sevenzip.File
}

// extract7zParallel extracts 7z files using a bounded worker pool.
// Pass 1 (sequential): create directories and build a list of file entries.
// Pass 2 (parallel): dispatch file writes to workers.
func (x *XFile) extract7zParallel(sevenZip *sevenzip.ReadCloser) (uint64, []string, []string, error) {
	entries, files, err := x.sevenZipPrepareEntries(sevenZip)
	if err != nil {
		return x.prog.Wrote, files, []string{x.FilePath}, err
	}

	workerErr := x.sevenZipDispatchWorkers(entries)
	if workerErr != nil {
		return x.prog.Wrote, files, []string{x.FilePath}, workerErr
	}

	files, err = x.cleanup(files)

	return x.prog.Wrote, files, []string{x.FilePath}, err
}

// sevenZipPrepareEntries iterates all entries, creates directories, validates paths,
// and returns the list of file entries to extract in parallel.
func (x *XFile) sevenZipPrepareEntries(sevenZip *sevenzip.ReadCloser) ([]sevenZipEntry, []string, error) {
	entries := make([]sevenZipEntry, 0, len(sevenZip.File))
	files := make([]string, 0, len(sevenZip.File))

	for _, zipFile := range sevenZip.File {
		cleanPath := x.clean(zipFile.Name)

		if !strings.HasPrefix(cleanPath, x.OutputDir) {
			return nil, files, fmt.Errorf("%s: %s: %w: %s (from: %s)",
				x.FilePath, zipFile.FileInfo().Name(), ErrInvalidPath, cleanPath, zipFile.Name)
		}

		files = append(files, filepath.Join(x.OutputDir, zipFile.Name))

		if zipFile.FileInfo().IsDir() {
			err := x.mkDir(cleanPath, zipFile.Mode(), zipFile.Modified)
			if err != nil {
				return nil, files, fmt.Errorf("%s: making 7z dir: %w", x.FilePath, err)
			}

			continue
		}

		entries = append(entries, sevenZipEntry{sevenZipFile: zipFile})
	}

	return entries, files, nil
}

// sevenZipDispatchWorkers sends file entries to a bounded worker pool for extraction.
func (x *XFile) sevenZipDispatchWorkers(entries []sevenZipEntry) error {
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

		waitGroup.Add(1)

		go func() {
			defer func() {
				<-semaphore // release worker slot
				waitGroup.Done()
			}()

			err := x.extract7zEntry(entry)
			if err != nil {
				errOnce.Do(func() { firstErr = err })
			}
		}()
	}

	waitGroup.Wait()

	return firstErr
}

// extract7zEntry extracts a single 7z file entry (used by parallel workers).
func (x *XFile) extract7zEntry(entry sevenZipEntry) error {
	zFile, err := entry.sevenZipFile.Open()
	if err != nil {
		return fmt.Errorf("%s: 7zFile.Open: %w", x.FilePath, err)
	}
	defer zFile.Close()

	fileInfo := &file{
		Path:     x.clean(entry.sevenZipFile.Name),
		Data:     zFile,
		FileMode: entry.sevenZipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    entry.sevenZipFile.Modified,
		Atime:    entry.sevenZipFile.Accessed,
	}

	_, err = x.writeParallel(fileInfo)
	if err != nil {
		return fmt.Errorf("%s: %w: %s (from: %s)",
			entry.sevenZipFile.FileInfo().Name(), err, fileInfo.Path, entry.sevenZipFile.Name)
	}

	return nil
}

func (x *XFile) un7zip(zipFile *sevenzip.File) (uint64, string, error) {
	zFile, err := zipFile.Open()
	if err != nil {
		return 0, zipFile.Name, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	file := &file{
		Path:     x.clean(zipFile.Name),
		Data:     zFile,
		FileMode: zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    zipFile.Modified,
		Atime:    zipFile.Accessed,
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		err := fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, file.Path, zipFile.Name)
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

	x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)",
		file.Path, zipFile.FileInfo().Size(), zipFile.UncompressedSize)

	s, err := x.write(file)
	if err != nil {
		return s, file.Path, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, file.Path, zipFile.Name)
	}

	return s, file.Path, nil
}
