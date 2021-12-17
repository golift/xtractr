package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/saracen/go7z"
)

// Extract7z extracts a 7zip archive. This wraps https://github.com/saracen/go7z.
func Extract7z(xFile *XFile) (int64, []string, error) {
	sevenZip, err := go7z.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer sevenZip.Close()

	return xFile.un7zip(sevenZip)
}

func (x *XFile) un7zip(szreader *go7z.ReadCloser) (int64, []string, error) {
	files := []string{}
	size := int64(0)

	for {
		header, err := szreader.Next()

		switch {
		case errors.Is(err, io.EOF):
			return size, files, nil
		case err != nil:
			return size, files, fmt.Errorf("szreader.Next: %w", err)
		case header == nil:
			return size, files, fmt.Errorf("%w: %s", ErrInvalidHead, x.FilePath)
		}

		wfile := x.clean(header.Name)
		if !strings.HasPrefix(wfile, x.OutputDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, wfile, header.Name)
		}

		// https://github.com/saracen/go7z/blob/9c09b6bd7fda869ef48ff6f693744a65f477816b/README.md#usage
		if header.IsEmptyStream && !header.IsEmptyFile {
			if err = os.MkdirAll(wfile, x.DirMode); err != nil {
				return size, files, fmt.Errorf("os.MkdirAll: %w", err)
			}

			continue
		}

		fileSize, err := writeFile(wfile, szreader, x.FileMode, x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += fileSize
	}
}
