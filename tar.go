package xtractr

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	lzw "github.com/sshaman1101/dcompress"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

// ExtractTar extracts a raw (non-compressed) tar archive.
func ExtractTar(xFile *XFile) (size int64, filesList []string, err error) {
	tarFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer tarFile.Close()

	return xFile.untar(tarFile)
}

// ExtractTarBzip extracts a bzip2-compressed tar archive.
func ExtractTarBzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	return xFile.untar(bzip2.NewReader(compressedFile))
}

// ExtractTarXZ extracts an XZ-compressed tar archive (txz).
func ExtractTarXZ(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipStream, err := xz.NewReader(compressedFile, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	return xFile.untar(zipStream)
}

// ExtractTarZ extracts an LZW-compressed tar archive (tz).
func ExtractTarZ(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipStream, err := lzw.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("lzw.NewReader: %w", err)
	}

	return xFile.untar(zipStream)
}

// ExtractTarGzip extracts a gzip-compressed tar archive (tgz).
func ExtractTarGzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipStream, err := gzip.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer zipStream.Close()

	return xFile.untar(zipStream)
}

// ExtractTarLzip extracts an LZIP-compressed tar archive (tlz).
func ExtractTarLzip(xFile *XFile) (size int64, filesList []string, err error) {
	compressedFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer compressedFile.Close()

	zipStream, err := lzma.NewReader(compressedFile)
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	return xFile.untar(zipStream)
}

func (x *XFile) untar(reader io.Reader) (int64, []string, error) {
	tarReader := tar.NewReader(reader)
	files := []string{}
	size := int64(0)

	for {
		header, err := tarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return size, files, fmt.Errorf("%s: tarReader.Next: %w", x.FilePath, err)
		}

		fSize, err := x.untarFile(header, tarReader)
		if err != nil {
			return size, files, err
		}

		files = append(files, header.Name)
		size += fSize
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes", header.Name, fSize, len(files), size)
	}

	files, err := x.cleanup(files)

	return size, files, err
}

func (x *XFile) untarFile(header *tar.Header, tarReader *tar.Reader) (int64, error) {
	file := &file{
		Path:     x.clean(header.Name),
		Data:     tarReader,
		FileMode: header.FileInfo().Mode(),
		DirMode:  x.DirMode,
		Mtime:    header.ChangeTime,
		Atime:    header.AccessTime,
	}

	if header.Format != tar.FormatGNU && header.Format != tar.FormatPAX {
		file.Mtime = header.ModTime
		file.Atime = time.Now()
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, file.Path, header.Name)
	}

	if header.Typeflag == tar.TypeDir {
		x.Debugf("Writing archived directory: %s", file.Path)

		err := x.mkDir(file.Path, header.FileInfo().Mode(), header.ModTime)
		if err != nil {
			return 0, fmt.Errorf("making tar file dir: %w", err)
		}

		return 0, nil
	}

	x.Debugf("Writing archived file: %s (bytes: %d)", file.Path, header.FileInfo().Size())

	return x.write(file)
}
