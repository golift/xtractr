package xtractr

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (size int64, filesList []string, err error) {
	zipReader, err := zip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	files := []string{}
	size = int64(0)

	for _, zipFile := range zipReader.File {
		fSize, wfile, err := xFile.unzip(zipFile)
		if err != nil {
			return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		//nolint:gosec // this is safe because we clean the paths.
		files = append(files, filepath.Join(xFile.OutputDir, zipFile.Name))
		size += fSize
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes", wfile, fSize, len(files), size)
	}

	files, err = xFile.cleanup(files)

	return size, files, err
}

func (x *XFile) unzip(zipFile *zip.File) (int64, string, error) {
	zFile, err := zipFile.Open()
	if err != nil {
		return 0, zipFile.Name, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	file := &file{
		Data:     zFile,
		FileMode: zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    zipFile.Modified,
		Atime:    time.Now(),
	}

	if file.Path, err = x.clean(zipFile.Name); err != nil {
		return 0, file.Path, err
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		err := fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, file.Path, zipFile.Name)
		return 0, file.Path, err
	}

	if zipFile.FileInfo().IsDir() {
		x.Debugf("Writing archived directory: %s", file.Path)

		if err := x.mkDir(file.Path, zipFile.Mode(), zipFile.Modified); err != nil {
			return 0, file.Path, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, file.Path, nil
	}

	x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)", file.Path,
		zipFile.CompressedSize64, zipFile.UncompressedSize64)

	s, err := x.write(file)
	if err != nil {
		return s, file.Path, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, file.Path, zipFile.Name)
	}

	return s, file.Path, nil
}
