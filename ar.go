package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/peterebden/ar"
)

// ExtractAr extracts a raw ar archive. Used by debian (.deb) packages.
func ExtractAr(xFile *XFile) (size int64, filesList []string, err error) {
	arFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer arFile.Close()

	return xFile.unAr(arFile)
}

func (x *XFile) unAr(reader io.Reader) (int64, []string, error) {
	arReader := ar.NewReader(reader)
	files := []string{}
	size := int64(0)

	for {
		header, err := arReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return size, files, fmt.Errorf("%s: arReader.Next: %w", x.FilePath, err)
		}

		wfile := x.clean(header.Name)
		if !strings.HasPrefix(wfile, x.OutputDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, wfile, header.Name)
		}

		// ar format does not store directory paths. Flat list of files.
		//nolint:gosec // we are not overflowing an integer with this conversion.
		fSize, err := writeFile(wfile, arReader, os.FileMode(header.Mode), x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += fSize
	}

	files, err := x.cleanup(files)

	return size, files, err
}
