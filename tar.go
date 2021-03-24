package xtractr

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractTar extracts a raw (non-compressed) tar archive.
func ExtractTar(x *XFile) (int64, []string, error) {
	tarFile, err := os.Open(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer tarFile.Close()

	return extractTarFile(x, tar.NewReader(tarFile))
}

// ExtractBzip extracts a bzip2-compressed file. That is, a single file.
func ExtractBzip(x *XFile) (int64, []string, error) {
	compressedFile, err := os.Open(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader := bzip2.NewReader(compressedFile)
	fileName := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(x.FilePath), ".bz"), ".bz2")
	wfile := filepath.Clean(filepath.Join(x.OutputDir, fileName))

	s, err := writeFile(wfile, zipReader, x.FileMode, x.DirMode)
	if err != nil {
		return s, nil, err
	}

	return s, []string{wfile}, nil
}

// ExtractGzip extracts a gzip-compressed file. That is, a single file.
func ExtractGzip(x *XFile) (int64, []string, error) {
	compressedFile, err := os.Open(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := gzip.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}

	fileName := strings.TrimSuffix(filepath.Base(x.FilePath), ".gz")
	wfile := filepath.Clean(filepath.Join(x.OutputDir, fileName))

	s, err := writeFile(wfile, zipReader, x.FileMode, x.DirMode)
	if err != nil {
		return s, nil, err
	}

	return s, []string{wfile}, nil
}

// ExtractTarBzip extracts a bzip2-compressed tar archive.
func ExtractTarBzip(x *XFile) (int64, []string, error) {
	compressedFile, err := os.Open(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	return extractTarFile(x, tar.NewReader(bzip2.NewReader(compressedFile)))
}

// ExtractTarGzip extracts a gzip-compressed tar archive.
func ExtractTarGzip(x *XFile) (int64, []string, error) {
	compressedFile, err := os.Open(x.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	gzipstream, err := gzip.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer gzipstream.Close()

	return extractTarFile(x, tar.NewReader(gzipstream))
}

func extractTarFile(x *XFile, tarReader *tar.Reader) (int64, []string, error) {
	files := []string{}
	size := int64(0)

	for {
		header, err := tarReader.Next()

		switch {
		case errors.Is(err, io.EOF):
			return size, files, nil
		case err != nil:
			return size, files, fmt.Errorf("tarReader.Next: %w", err)
		case header == nil:
			return size, files, fmt.Errorf("%w: %s", ErrInvalidHead, x.FilePath)
		}

		wfile := filepath.Clean(filepath.Join(x.OutputDir, header.Name)) //nolint:gosec
		if !strings.HasPrefix(wfile, x.OutputDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return size, files, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, wfile, header.Name)
		}

		if header.Typeflag == tar.TypeDir {
			if err = os.MkdirAll(wfile, header.FileInfo().Mode()); err != nil {
				return size, files, fmt.Errorf("os.MkdirAll: %w", err)
			}

			continue
		}

		s, err := writeFile(wfile, tarReader, header.FileInfo().Mode(), x.DirMode)
		if err != nil {
			return size, files, err
		}

		files = append(files, wfile)
		size += s
	}
}
