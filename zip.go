package extractorr

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExtractZIP extracts a zip file.. to a destination.
func ExtractZIP(source string, destination string) (int64, []string, error) {
	r, err := zip.OpenReader(source)
	if err != nil {
		return 0, nil, err
	}
	defer r.Close()

	files := []string{}
	size := int64(0)

	for _, zf := range r.Reader.File {
		s, err := unzipFile(zf, destination)
		if err != nil {
			return size, files, err
		}

		files = append(files, filepath.Join(destination, zf.Name))
		size += s
	}

	return size, files, nil
}

func unzipFile(zf *zip.File, destination string) (int64, error) {
	if strings.Contains(zf.Name, "../") || (runtime.GOOS == "windows" && strings.Contains(zf.Name, `..\`)) {
		return 0, fmt.Errorf("archived file contains invalid file path: %v", zf.Name)
	}

	if strings.HasSuffix(zf.Name, "/") {
		return 0, os.MkdirAll(filepath.Join(destination, zf.Name), 0755)
	}

	rc, err := zf.Open()
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	return WriteNewFile(filepath.Join(destination, zf.Name), rc, zf.FileInfo().Mode())
}
