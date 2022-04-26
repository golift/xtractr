package xtractr

/* How to extract a RAR file. */

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode"
)

func ExtractRAR(xFile *XFile) (int64, []string, []string, error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		return extractRAR(xFile)
	}

	// Try all the passwords.
	passwords := append(xFile.Passwords, xFile.Password) // nolint:gocritic
	for idx, password := range passwords {
		size, files, archives, err := extractRAR(&XFile{
			FilePath:  xFile.FilePath,
			OutputDir: xFile.OutputDir,
			FileMode:  xFile.FileMode,
			DirMode:   xFile.DirMode,
			Password:  password,
		})
		if err == nil {
			return size, files, archives, nil
		}

		// https://github.com/nwaples/rardecode/issues/28
		if strings.Contains(err.Error(), "incorrect password") {
			continue
		}

		return size, files, archives, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
	}

	// No password worked, try without a password.
	return extractRAR(&XFile{
		FilePath:  xFile.FilePath,
		OutputDir: xFile.OutputDir,
		FileMode:  xFile.FileMode,
		DirMode:   xFile.DirMode,
	})
}

// ExtractRAR extracts a rar file. to a destination. This wraps github.com/nwaples/rardecode.
func extractRAR(xFile *XFile) (int64, []string, []string, error) {
	rarReader, err := rardecode.OpenReader(xFile.FilePath, xFile.Password)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	size, files, err := xFile.unrar(rarReader)

	return size, files, rarReader.Volumes(), err
}

func (x *XFile) unrar(rarReader *rardecode.ReadCloser) (int64, []string, error) {
	files := []string{}
	size := int64(0)

	for {
		header, err := rarReader.Next()

		switch {
		case errors.Is(err, io.EOF):
			return size, files, nil
		case err != nil:
			return size, files, fmt.Errorf("rarReader.Next: %w", err)
		case header == nil:
			return size, files, fmt.Errorf("%w: %s", ErrInvalidHead, x.FilePath)
		}

		wfile := x.clean(header.Name)
		// nolint:gocritic // this 1-argument filepath.Join removes a ./ prefix should there be one.
		if !strings.HasPrefix(wfile, filepath.Join(x.OutputDir)) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("%s: %w: %s != %s (from: %s)",
				x.FilePath, ErrInvalidPath, wfile, x.OutputDir, header.Name)
		}

		if header.IsDir {
			if err = os.MkdirAll(wfile, x.DirMode); err != nil {
				return size, files, fmt.Errorf("os.MkdirAll: %w", err)
			}

			continue
		}

		if err = os.MkdirAll(filepath.Dir(wfile), x.DirMode); err != nil {
			return size, files, fmt.Errorf("os.MkdirAll: %w", err)
		}

		fSize, err := writeFile(wfile, rarReader, x.FileMode, x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += fSize
	}
}
