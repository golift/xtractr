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
func ExtractZIP(path string, to string) (int64, []string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()

	files := []string{}
	size := int64(0)

	for _, zf := range r.Reader.File {
		s, err := unzipFile(zf, to)
		if err != nil {
			return size, files, err
		}

		files = append(files, filepath.Join(to, zf.Name))
		size += s
	}

	return size, files, nil
}

func unzipFile(zf *zip.File, to string) (int64, error) {
	rfile := filepath.Clean(filepath.Join(to, zf.Name))
	if !strings.HasPrefix(rfile, to) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("archived file contains invalid path: %s (from: %s)",
			rfile, zf.Name)
	}

	if strings.HasSuffix(rfile, "/") {
		return 0, os.MkdirAll(rfile, 0755)
	}

	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	return writeFile(rfile, rc, zf.FileInfo().Mode())
}
