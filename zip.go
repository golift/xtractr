package xtractr

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	zipp "github.com/alexmullins/zip"
)

type FileInfo interface {
	Name() string
	IsDir() bool
}

type zipFile struct {
	Name     string
	FileInfo FileInfo
	Open     func() (io.ReadCloser, error)
}

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (int64, []string, error) {
	var (
		zipReader io.Closer
		err       error
	)
	if xFile.Password == "" {
		zipReader, err = zip.OpenReader(xFile.FilePath)
	} else {
		zipReader, err = zipp.OpenReader(xFile.FilePath)
	}

	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}

	defer zipReader.Close()
	files := []string{}
	size := int64(0)

	var zf *zipFile
	if zr, ok := zipReader.(*zip.ReadCloser); ok && zr != nil {
		for _, zFile := range zr.Reader.File {
			zf = &zipFile{Name: zFile.Name, FileInfo: zFile.FileInfo(), Open: zFile.Open}
			fSize, err := xFile.unzip(zf)
			if err != nil {
				return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
			}

			files = append(files, filepath.Join(xFile.OutputDir, zFile.Name)) //nolint: gosec
			size += fSize
		}
	} else if zr, ok := zipReader.(*zipp.ReadCloser); ok && zr != nil {
		for _, zFile := range zr.Reader.File {
			// Password.
			if zFile.IsEncrypted() {
				zFile.SetPassword(xFile.Password)
			}
			zf = &zipFile{Name: zFile.Name, FileInfo: zFile.FileInfo(), Open: zFile.Open}

			fSize, err := xFile.unzip(zf)
			if err != nil {
				return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
			}

			files = append(files, filepath.Join(xFile.OutputDir, zFile.Name)) //nolint: gosec
			size += fSize
		}
	}
	return size, files, nil
}

func (x *XFile) unzip(zipFile *zipFile) (int64, error) {
	wfile := x.clean(zipFile.Name)
	if !strings.HasPrefix(wfile, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo.Name(), ErrInvalidPath, wfile, zipFile.Name)
	}

	if strings.HasSuffix(wfile, "/") || zipFile.FileInfo.IsDir() {
		if err := os.MkdirAll(wfile, x.DirMode); err != nil {
			return 0, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, nil
	}

	zFile, err := zipFile.Open()
	if err != nil {
		return 0, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	s, err := writeFile(wfile, zFile, x.FileMode, x.DirMode)
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo.Name(), err, wfile, zipFile.Name)
	}

	return s, nil
}
