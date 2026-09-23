package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golift.io/asar"
)

// ExtractASAR extracts an Electron ASAR archive. archiveList is the archive
// plus {archive}.unpacked when the index consumed files from that sibling.
// DeleteOrig removes every path in that list.
func ExtractASAR(xFile *XFile) (size uint64, filesList, archiveList []string, err error) {
	reader, err := asar.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s: asar.Open: %w", xFile.FilePath, err)
	}
	defer closeNamed(reader, &err)

	tracker, headerErr := xFile.archiveProgress(asarProgress(reader, xFile.FilePath))
	defer tracker.done()

	if headerErr != nil {
		return 0, nil, nil, headerErr
	}

	archiveList = asarArchiveList(xFile.FilePath, reader.Files)

	entries, files, err := xFile.asarPrepareEntries(reader)
	if err != nil {
		return xFile.prog.Wrote, files, archiveList, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	err = xFile.extractASARFiles(entries)
	if err != nil {
		return xFile.prog.Wrote, files, archiveList, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	files, err = xFile.cleanup(files)

	return xFile.prog.Wrote, files, archiveList, err
}

// asarArchiveList is the paths DeleteOrig should remove. The unpacked sibling
// is included only when a file entry was read from it.
func asarArchiveList(filePath string, files []*asar.File) []string {
	list := []string{filePath}

	for _, file := range files {
		if file != nil && file.Unpacked && !file.IsDir() && !file.IsLink() {
			return append(list, filePath+".unpacked")
		}
	}

	return list
}

func asarProgress(reader *asar.Reader, filePath string) (total, compressed uint64, count int) {
	for _, file := range reader.Files {
		count++

		if file.IsDir() || file.IsLink() {
			continue
		}

		total += uint64(file.Size)
	}

	return total, archiveFileSize(filePath), count
}

type asarFileEntry struct {
	file *asar.File
}

func (x *XFile) asarPrepareEntries(reader *asar.Reader) ([]asarFileEntry, []string, error) {
	entries := make([]asarFileEntry, 0, len(reader.Files))
	files := make([]string, 0, len(reader.Files))

	for _, asarFile := range reader.Files {
		cleanPath := x.clean(asarFile.Name)

		if !x.pathWithinOutput(cleanPath) {
			return nil, files, fmt.Errorf("%s: %w: %s", asarFile.Name, ErrInvalidPath, cleanPath)
		}

		files = append(files, filepath.Join(x.OutputDir, filepath.FromSlash(asarFile.Name)))

		switch {
		case asarFile.IsDir():
			err := x.mkDir(cleanPath, x.DirMode, time.Now())
			if err != nil {
				return nil, files, fmt.Errorf("making asar dir: %w", err)
			}
		case asarFile.IsLink():
			err := x.mkDir(filepath.Dir(cleanPath), x.DirMode, time.Now())
			if err != nil {
				return nil, files, fmt.Errorf("making asar symlink parent: %w", err)
			}

			err = x.createASARSymlink(cleanPath, asarFile.Link)
			if err != nil {
				return nil, files, err
			}
		default:
			entries = append(entries, asarFileEntry{file: asarFile})
		}
	}

	return entries, files, nil
}

func (x *XFile) createASARSymlink(linkPath, packageLink string) error {
	target := filepath.Join(x.OutputDir, filepath.FromSlash(packageLink))
	if !x.pathWithinOutput(target) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, target, packageLink)
	}

	rel, err := filepath.Rel(filepath.Dir(linkPath), target)
	if err != nil {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, target, packageLink)
	}

	return x.createSymlink(linkPath, rel)
}

func (x *XFile) extractASARFiles(entries []asarFileEntry) error {
	if x.FileWorkers > 1 {
		return dispatchWorkers(x.FileWorkers, entries, x.extractASAREntry)
	}

	for _, entry := range entries {
		err := x.writeASARFile(entry.file, false)
		if err != nil {
			return err
		}
	}

	return nil
}

func (x *XFile) extractASAREntry(entry asarFileEntry) error {
	return x.writeASARFile(entry.file, true)
}

func (x *XFile) writeASARFile(asarFile *asar.File, parallel bool) (err error) {
	src, err := x.openASARFile(asarFile)
	if err != nil {
		return err
	}

	fileInfo := &file{
		Path:     x.clean(asarFile.Name),
		Data:     src,
		FileMode: x.asarFileMode(asarFile),
		DirMode:  x.DirMode,
		Mtime:    time.Now(),
		Atime:    time.Now(),
	}

	if parallel {
		_, err = x.writeParallel(fileInfo)
	} else {
		_, err = x.write(fileInfo)
	}

	closeNamed(src, &err)

	if err != nil {
		_ = os.Remove(fileInfo.Path)

		return fmt.Errorf("%s: %w: %s", asarFile.Name, err, fileInfo.Path)
	}

	return nil
}

func (x *XFile) openASARFile(asarFile *asar.File) (io.ReadCloser, error) {
	if asarFile.Unpacked {
		return x.openUnpackedASAR(asarFile.Name)
	}

	src, err := asarFile.Open()
	if err != nil {
		return nil, fmt.Errorf("%s: asar.File.Open: %w", asarFile.Name, err)
	}

	return io.NopCloser(src), nil
}

// unpackedRootFile closes the member and the confined root it was opened from.
type unpackedRootFile struct {
	*os.File

	root *os.Root
}

func (f *unpackedRootFile) Close() error {
	return errors.Join(f.File.Close(), f.root.Close())
}

// openUnpackedASAR reads one member from {archive}.unpacked. os.OpenRoot keeps
// the open inside that directory, including through symlinks. A symlink, FIFO,
// or other non-regular file is refused before any bytes are copied.
func (x *XFile) openUnpackedASAR(name string) (io.ReadCloser, error) {
	root, err := os.OpenRoot(x.FilePath + ".unpacked")
	if err != nil {
		return nil, fmt.Errorf("unpacked entry %s: %w", name, err)
	}

	src, err := openRegularUnpacked(root, name)
	if err != nil {
		_ = root.Close()

		return nil, err
	}

	return &unpackedRootFile{File: src, root: root}, nil
}

func openRegularUnpacked(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("unpacked entry %s: %w", name, err)
	}

	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("unpacked entry %s: %w", name, ErrInvalidPath)
	}

	src, err := root.Open(name)
	if err != nil {
		return nil, fmt.Errorf("unpacked entry %s: %w", name, err)
	}

	err = requireUnpackedRegular(src)
	if err != nil {
		_ = src.Close()

		return nil, fmt.Errorf("unpacked entry %s: %w", name, err)
	}

	return src, nil
}

func requireUnpackedRegular(src *os.File) error {
	info, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	if !info.Mode().IsRegular() {
		return ErrInvalidPath
	}

	err = requireDiskFile(src)
	if err != nil {
		return ErrInvalidPath
	}

	return nil
}

func (x *XFile) asarFileMode(asarFile *asar.File) os.FileMode {
	mode := x.FileMode
	if asarFile.Executable {
		mode |= 0o111
	}

	return mode
}
