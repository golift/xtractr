package xtractr

/* How to extract a RAR file. */

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode"
)

// ExtractRAR extracts a rar file.. to a destination. Simple enough.
func ExtractRAR(rarFilePath string, toDir string) (int64, []string, error) {
	rarReader, err := rardecode.OpenReader(rarFilePath, "")
	if err != nil {
		return 0, nil, err
	}

	files := []string{}
	size := int64(0)

	for {
		header, err := rarReader.Next()
		if err == io.EOF {
			break
		} else if err != nil {
			return size, files, err
		} else if header == nil {
			return size, files, fmt.Errorf("rar file '%s' contains invalid file header", rarFilePath)
		}

		wfile := filepath.Clean(filepath.Join(toDir, header.Name))
		if !strings.HasPrefix(wfile, toDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("archived file '%s' contains invalid path: %s (from: %s)",
				rarFilePath, wfile, header.Name)
		}

		if header.IsDir {
			if err = os.MkdirAll(wfile, 0755); err != nil {
				return size, files, err
			}

			continue
		}

		if err = os.MkdirAll(filepath.Dir(wfile), 0755); err != nil {
			return size, files, err
		}

		s, err := writeFile(wfile, rarReader, header.Mode())
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += s
	}

	return size, files, nil
}
