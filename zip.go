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
func ExtractZIP(x *XFile) (int64, []string, error) {
	zipReader, err := zip.OpenReader(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	files := []string{}
	size := int64(0)

	for _, zf := range zipReader.Reader.File {
		s, err := unzipFile(zf, x.OutputDir, x.FileMode, x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, filepath.Join(x.OutputDir, zf.Name)) // nolint: gosec
		size += s
	}

	return size, files, nil
}

func unzipFile(zipFile *zip.File, toPath string, fm, dm os.FileMode) (int64, error) {
	rfile := filepath.Clean(filepath.Join(toPath, zipFile.Name)) // nolint: gosec
	if !strings.HasPrefix(rfile, toPath) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, rfile, zipFile.Name)
	}

	if strings.HasSuffix(rfile, "/") || zipFile.FileInfo().IsDir() {
		if err := os.MkdirAll(rfile, dm); err != nil {
			return 0, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, nil
	}

	rc, err := zipFile.Open()
	if err != nil {
		return 0, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer rc.Close()

	s, err := writeFile(rfile, rc, fm, dm)
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, rfile, zipFile.Name)
	}

	return s, nil
}
