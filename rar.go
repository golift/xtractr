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

// ExtractRAR extracts a rar file. to a destination. This wraps github.com/nwaples/rardecode.
func ExtractRAR(xFile *XFile) (int64, []string, []string, error) {
	rarReader, err := openRAR(xFile)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	size, files, err := xFile.unrar(rarReader)

	return size, files, rarReader.Volumes(), err
}

// openRAR tries multiple passwords.
func openRAR(xFile *XFile) (*rardecode.ReadCloser, error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		// No passwords provided.
		rarReader, err := rardecode.OpenReader(xFile.FilePath, "")
		if err != nil {
			return rarReader, fmt.Errorf("rardecode.OpenReader: %w", err)
		}

		return rarReader, nil
	}

	// Try all the passwords.
	passwords := append(xFile.Passwords, xFile.Password)

	for idx, password := range passwords {
		rarReader, err := rardecode.OpenReader(xFile.FilePath, password)
		if err != nil {
			// https://github.com/nwaples/rardecode/issues/28
			if strings.Contains(err.Error(), "bad password") {
				continue
			}

			return rarReader, fmt.Errorf("used password %d of %d, rardecode.OpenReader: %w", idx+1, len(passwords), err)
		}

		return rarReader, nil
	}

	// No password worked, try without a password.
	rarReader, err := rardecode.OpenReader(xFile.FilePath, "")
	if err != nil {
		return rarReader, fmt.Errorf("after trying %d passwords, rardecode.OpenReader: %w", len(passwords), err)
	}

	return rarReader, nil
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
		if !strings.HasPrefix(wfile, x.OutputDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, wfile, header.Name)
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
