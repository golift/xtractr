package xtractr

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Extract7z extracts a 7zip archive.
// Volumes: https://github.com/bodgit/sevenzip/issues/54
func Extract7z(xFile *XFile) (size int64, filesList, archiveList []string, err error) {
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
			FilePath:  xFile.FilePath,
			OutputDir: xFile.OutputDir,
			FileMode:  xFile.FileMode,
			DirMode:   xFile.DirMode,
			Password:  password,
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

func extract7z(xFile *XFile) (int64, []string, []string, error) {
	var (
		sevenZip *sevenzip.ReadCloser
		err      error
	)

	if xFile.Password != "" {
		sevenZip, err = sevenzip.OpenReaderWithPassword(xFile.FilePath, xFile.Password)
	} else {
		sevenZip, err = sevenzip.OpenReader(xFile.FilePath)
	}

	if err != nil {
		return 0, nil, nil, fmt.Errorf("%s: os.Open: %w", xFile.FilePath, err)
	}

	defer sevenZip.Close()

	files := []string{}
	size := int64(0)

	for _, zipFile := range sevenZip.File {
		fSize, wfile, err := xFile.un7zip(zipFile)
		if err != nil {
			lastFile := xFile.FilePath
			/* // https://github.com/bodgit/sevenzip/issues/54
			// We can probably never get the file with the error.
			if volumes := sevenZip.Volumes(); len(volumes) > 0 {
				lastFile = volumes[len(volumes)-1]
			} */
			return size, files, sevenZip.Volumes(), fmt.Errorf("%s: %w", lastFile, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, zipFile.Name))
		size += fSize
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes", wfile, fSize, len(files), size)
	}

	files, err = xFile.cleanup(files)

	return size, files, sevenZip.Volumes(), err
}

func (x *XFile) un7zip(zipFile *sevenzip.File) (int64, string, error) {
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
