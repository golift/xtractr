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
func ExtractZIP(zipFile string, toPath string) (int64, []string, error) {
	zipReader, err := zip.OpenReader(zipFile)
	if err != nil {
		return 0, nil, err
	}
	defer zipReader.Close()

	files := []string{}
	size := int64(0)

	for _, zf := range zipReader.Reader.File {
		s, err := unzipFile(zf, toPath)
		if err != nil {
			return size, files, err
		}

		files = append(files, filepath.Join(toPath, zf.Name))
		size += s
	}

	return size, files, nil
}

func unzipFile(zipFile *zip.File, toPath string) (int64, error) {
	rfile := filepath.Clean(filepath.Join(toPath, zipFile.Name))
	if !strings.HasPrefix(rfile, toPath) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("archived file '%s' contains invalid path: %s (from: %s)",
			zipFile.FileInfo().Name(), rfile, zipFile.Name)
	}

	if strings.HasSuffix(rfile, "/") {
		return 0, os.MkdirAll(rfile, 0755)
	}

	rc, err := zipFile.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	return writeFile(rfile, rc, zipFile.FileInfo().Mode())
}
