package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// Extract7z extracts a 7zip archive.
// Volumes: https://github.com/bodgit/sevenzip/issues/54
func Extract7z(xFile *XFile) (int64, []string, error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		return extract7z(xFile)
	}

	// Try all the passwords.
	passwords := xFile.Passwords

	if xFile.Password != "" { // If a single password is provided, try it first.
		passwords = append([]string{xFile.Password}, xFile.Passwords...)
	}

	for idx, password := range passwords {
		size, files, err := extract7z(&XFile{
			FilePath:  xFile.FilePath,
			OutputDir: xFile.OutputDir,
			FileMode:  xFile.FileMode,
			DirMode:   xFile.DirMode,
			Password:  password,
		})
		if err != nil && idx == len(passwords)-1 {
			return size, files, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
		} else if err == nil {
			return size, files, nil
		}
	}

	// unreachable code
	return 0, nil, nil
}

func extract7z(xFile *XFile) (int64, []string, error) {
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
		return 0, nil, fmt.Errorf("%s: os.Open: %w", xFile.FilePath, err)
	}

	defer sevenZip.Close()

	files := []string{}
	size := int64(0)

	for _, zipFile := range sevenZip.File {
		fSize, err := xFile.un7zip(zipFile)
		if err != nil {
			return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
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
