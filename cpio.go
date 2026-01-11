package xtractr

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/cavaliergopher/cpio"
)

// ExtractCPIOGzip extracts a gzip-compressed cpio archive (cpgz).
func ExtractCPIOGzip(xFile *XFile) (size uint64, filesList []string, err error) {
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

	files, err := xFile.uncpio(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractCPIO extracts a .cpio file.
func ExtractCPIO(xFile *XFile) (size uint64, filesList []string, err error) {
	fileReader, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer fileReader.Close()

	defer xFile.newProgress(uint64(stat.Size()), uint64(stat.Size()), 0).done()

	files, err := xFile.uncpio(xFile.prog.reader(fileReader))

	return xFile.prog.Wrote, files, err
}

func (x *XFile) uncpio(reader io.Reader) ([]string, error) {
	zipReader := cpio.NewReader(reader)
	files := []string{}

	for {
		zipFile, err := zipReader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		} else if err != nil {
			return nil, fmt.Errorf("cpio Next() failed: %w", err)
		}

		fSize, err := x.uncpioFile(zipFile, zipReader)
		if err != nil {
			return files, fmt.Errorf("%s: %w", x.FilePath, err)
		}

		files = append(files, filepath.Join(x.OutputDir, zipFile.Name))
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			zipFile.Name, fSize, x.prog.Files, x.prog.Wrote)
	}
}

func (x *XFile) uncpioFile(cpioFile *cpio.Header, cpioReader *cpio.Reader) (uint64, error) {
	file := &file{
		Path:     x.clean(cpioFile.Name),
		Data:     cpioReader,
		FileMode: cpioFile.FileInfo().Mode(),
		DirMode:  x.DirMode,
		Mtime:    cpioFile.ModTime,
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of the base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", cpioFile.FileInfo().Name(), ErrInvalidPath, file.Path, cpioFile.Name)
	}

	if cpioFile.Mode.IsDir() || cpioFile.FileInfo().IsDir() {
		err := x.mkDir(file.Path, cpioFile.FileInfo().Mode(), cpioFile.ModTime)
		if err != nil {
			return 0, fmt.Errorf("making cpio dir: %w", err)
		}

		return 0, nil
	}

	// This turns hard links into symlinks.
	if cpioFile.Linkname != "" {
		err := os.Symlink(cpioFile.Linkname, file.Path)
		if err != nil {
			return 0, fmt.Errorf("%s symlink: %w: %s (from: %s)", cpioFile.FileInfo().Name(), err, file.Path, cpioFile.Name)
		}

		return 0, nil
	}

	// This should turn non-regular files into empty files.
	// ie. sockets, block, character and fifo devices.
	s, err := x.write(file)
	if err != nil {
		return s, fmt.Errorf("%s: %w: %s (from: %s)", cpioFile.FileInfo().Name(), err, file.Path, cpioFile.Name)
	}

	return s, nil
}
