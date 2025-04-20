package xtractr

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	lzw "github.com/sshaman1101/dcompress"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

// ExtractTar extracts a raw (non-compressed) tar archive.
func ExtractTar(xFile *XFile) (size uint64, filesList []string, err error) {
	tarFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer tarFile.Close()

	defer xFile.newProgress(uint64(stat.Size()), uint64(stat.Size()), 0).done()

	files, err := xFile.untar(xFile.prog.reader(tarFile))

	return xFile.prog.Wrote, files, err
}

// ExtractTarBzip extracts a bzip2-compressed tar archive.
func ExtractTarBzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	files, err := xFile.untar(bzip2.NewReader(xFile.prog.reader(compressedFile)))

	return xFile.prog.Wrote, files, err
}

// ExtractTarXZ extracts an XZ-compressed tar archive (txz).
func ExtractTarXZ(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := xz.NewReader(xFile.prog.reader(compressedFile), 0)
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarZ extracts an LZW-compressed tar archive (tz).
func ExtractTarZ(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := lzw.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("lzw.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarGzip extracts a gzip-compressed tar archive (tgz).
func ExtractTarGzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := gzip.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer zipStream.Close()

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarLzip extracts an LZIP-compressed tar archive (tlz).
func ExtractTarLzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := lzma.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

func (x *XFile) untar(reader io.Reader) ([]string, error) {
	tarReader := tar.NewReader(reader)
	files := []string{}

	for {
		header, err := tarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return files, fmt.Errorf("%s: tarReader.Next: %w", x.FilePath, err)
		}

		fSize, err := x.untarFile(header, tarReader)
		if err != nil {
			return files, err
		}

		files = append(files, header.Name)
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			header.Name, fSize, x.prog.Files, x.prog.Wrote)
	}

	files, err := x.cleanup(files)

	return files, err
}

func (x *XFile) untarFile(header *tar.Header, tarReader *tar.Reader) (uint64, error) {
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

		if err := x.mkDir(file.Path, header.FileInfo().Mode(), header.ModTime); err != nil {
			return 0, fmt.Errorf("making tar file dir: %w", err)
		}

		return 0, nil
	}

	x.Debugf("Writing archived file: %s (bytes: %d)", file.Path, header.FileInfo().Size())

	return x.write(file)
}
