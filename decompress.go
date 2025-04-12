package xtractr

import (
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"os"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zlib"
	"github.com/klauspost/compress/zstd"
	"github.com/pierrec/lz4/v4"
	lzw "github.com/sshaman1101/dcompress"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

// ExtractXZ extracts an XZ-compressed file. A single file.
func ExtractXZ(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := xz.NewReader(compressedFile, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".xz")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractZlib extracts a zlib-compressed file. A single file.
func ExtractZlib(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := zlib.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("zlib.NewReader: %w", err)
	}
	defer zipReader.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".zz", ".zlib")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractLZMA extracts an lzma-compressed file. A single file.
func ExtractLZMA(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := lzma.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("lzma.NewReader: %w", err)
	}

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".lzma", ".lz", ".lzip")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractLZMA2 extracts an lzma2-compressed file. A single file.
func ExtractLZMA2(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := lzma.NewReader2(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("lzma.NewReader2: %w", err)
	}

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".lzma", ".lzma2")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractZstandard extracts a Zstandard-compressed file. A single file.
func ExtractZstandard(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := zstd.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("zstd.NewReader: %w", err)
	}
	defer zipReader.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".zstd", ".zst")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractLZW extracts an LZW-compressed file. A single file.
func ExtractLZW(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := lzw.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("lzw.NewReader: %w", err)
	}

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".Z")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractLZ4 extracts an LZ4-compressed file. A single file.
func ExtractLZ4(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".lz4")

	size, err = writeFile(wfile, lz4.NewReader(compressedFile), xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractSnappy extracts a snappy-compressed file. A single file.
func ExtractSnappy(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".snappy", ".sz")

	size, err = writeFile(wfile, snappy.NewReader(compressedFile), xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractS2 extracts a Snappy2-compressed file. A single file.
func ExtractS2(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".s2")

	size, err = writeFile(wfile, s2.NewReader(compressedFile), xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractBrotli extracts a Brotli-compressed file. A single file.
func ExtractBrotli(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".brotli", ".br")

	size, err = writeFile(wfile, brotli.NewReader(compressedFile), xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractBzip extracts a bzip2-compressed file. That is, a single file.
func ExtractBzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".bz", ".bz2")

	size, err = writeFile(wfile, bzip2.NewReader(compressedFile), xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}

// ExtractGzip extracts a gzip-compressed file. That is, a single file.
func ExtractGzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipReader, err := gzip.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer zipReader.Close()

	// Get the absolute path of the file being written.
	wfile := xFile.clean(xFile.FilePath, ".gz")

	size, err = writeFile(wfile, zipReader, xFile.FileMode, xFile.DirMode)
	if err != nil {
		return size, nil, err
	}

	return size, []string{wfile}, nil
}
