package xtractr

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode"
)

// ExtractRAR extracts a rar file.. to a destination.
func ExtractRAR(path string, to string) (int64, []string, error) {
	rr, err := rardecode.OpenReader(path, "")
	if err != nil {
		return 0, nil, fmt.Errorf("creating reader: %v", err)
	}

	files := []string{}
	size := int64(0)

	//sum := 1
	for {
		//sum += sum
		header, err := rr.Next()
		if err == io.EOF {
			break
		}

		rfile := filepath.Clean(filepath.Join(to, header.Name))
		if !strings.HasPrefix(rfile, to) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("archived file contains invalid file path: %s (from: %s)",
				rfile, header.Name)
		}

		if header.IsDir {
			if err = os.MkdirAll(rfile, 0755); err != nil {
				return size, files, err
			}

			continue
		}

		if err = os.MkdirAll(filepath.Dir(rfile), 0755); err != nil {
			return size, files, err
		}

		s, err := writeFile(rfile, rr, header.Mode())
		if err != nil {
			return size, files, err
		}

		files = append(files, rfile)
		size += s
	}

	return size, files, nil
}
