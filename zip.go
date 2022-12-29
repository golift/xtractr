package xtractr

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (int64, []string, error) {
	zipReader, err := zip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	files := []string{}
	size := int64(0)

	for _, zipFile := range zipReader.Reader.File {
		fSize, err := xFile.unzip(zipFile)
		if err != nil {
			return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, zipFile.Name)) //nolint: gosec
		size += fSize
	}

	return size, files, nil
}

func (x *XFile) unzip(zipFile *zip.File) (int64, error) { //nolint:dupl
	wfile := x.clean(zipFile.Name)
	if !strings.HasPrefix(wfile, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, wfile, zipFile.Name)
	}

	if strings.HasSuffix(wfile, "/") || zipFile.FileInfo().IsDir() {
		if err := os.MkdirAll(wfile, x.DirMode); err != nil {
			return 0, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, nil
	}

	zFile, err := zipFile.Open()
	if err != nil {
		return 0, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	s, err := writeFile(wfile, zFile, x.FileMode, x.DirMode)
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, wfile, zipFile.Name)
	}

	return s, nil
}
