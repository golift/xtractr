package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Extract7z extracts a 7zip archive. This wraps https://github.com/saracen/go7z.
func Extract7z(xFile *XFile) (int64, []string, error) {
	sevenZip, err := sevenzip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer sevenZip.Close()

	files := []string{}
	size := int64(0)

	for _, zipFile := range sevenZip.File {
		fSize, err := xFile.un7zip(zipFile)
		if err != nil {
			return size, files, err
		}

		files = append(files, filepath.Join(xFile.OutputDir, zipFile.Name)) // nolint: gosec
		size += fSize
	}

	return size, files, nil
}

func (x *XFile) un7zip(zipFile *sevenzip.File) (int64, error) {
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

type ZipFile interface {
	FileInfo() os.FileInfo
	Name() string
}
