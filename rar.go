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

// ExtractRAR extracts a rar file.. to a destination. Simple enough.
func ExtractRAR(x *XFile) (int64, []string, []string, error) {
	rarReader, err := rardecode.OpenReader(x.FilePath, x.Password)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	size, files, err := x.unrar(rarReader)

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

		s, err := writeFile(wfile, rarReader, x.FileMode, x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += s
	}
}
