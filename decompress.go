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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".xz"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".zz", ".zlib"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".lzma", ".lz", ".lzip"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".lzma", ".lzma2"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".zstd", ".zst"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".Z"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}

// ExtractLZ4 extracts an LZ4-compressed file. A single file.
func ExtractLZ4(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".lz4"),
		Data:     lz4.NewReader(compressedFile),
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}

// ExtractSnappy extracts a snappy-compressed file. A single file.
func ExtractSnappy(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".snappy", ".sz"),
		Data:     snappy.NewReader(compressedFile),
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}

// ExtractS2 extracts a Snappy2-compressed file. A single file.
func ExtractS2(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".s2"),
		Data:     s2.NewReader(compressedFile),
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}

// ExtractBrotli extracts a Brotli-compressed file. A single file.
func ExtractBrotli(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".brotli", ".br"),
		Data:     brotli.NewReader(compressedFile),
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}

// ExtractBzip extracts a bzip2-compressed file. That is, a single file.
func ExtractBzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	// Get the absolute path of the file being written.
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".bz", ".bz2"),
		Data:     bzip2.NewReader(compressedFile),
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
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
	file := &file{
		Path:     xFile.clean(xFile.FilePath, ".gz"),
		Data:     zipReader,
		FileMode: xFile.FileMode,
		DirMode:  xFile.DirMode,
		Mtime:    zipReader.ModTime,
	}

	size, err = file.Write()
	if err != nil {
		return size, nil, err
	}

	return size, []string{file.Path}, nil
}
