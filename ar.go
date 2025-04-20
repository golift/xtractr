package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/peterebden/ar"
)

// ExtractAr extracts a raw ar archive. Used by debian (.deb) packages.
func ExtractAr(xFile *XFile) (size uint64, filesList []string, err error) {
	arFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}

	defer xFile.newProgress(getUncompressedArSize(arFile)).done() // this closes arFile

	if arFile, err = os.Open(xFile.FilePath); err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}

	defer arFile.Close()

	files, err := xFile.unAr(xFile.prog.reader(arFile))

	return xFile.prog.Wrote, files, err
}

func (x *XFile) unAr(reader io.Reader) ([]string, error) {
	arReader := ar.NewReader(reader)
	files := []string{}

	for {
		header, err := arReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return files, fmt.Errorf("%s: arReader.Next: %w", x.FilePath, err)
		}

		file := &file{
			Path:     x.clean(header.Name),
			Data:     arReader,
			FileMode: os.FileMode(header.Mode),
			DirMode:  x.DirMode,
			Mtime:    header.ModTime,
		}

		if !strings.HasPrefix(file.Path, x.OutputDir) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return files, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, file.Path, header.Name)
		}

		// ar format does not store directory paths. Flat list of files.

		fSize, err := x.write(file)
		if err != nil {
			return files, err
		}

		files = append(files, file.Path)
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			file.Path, fSize, x.prog.Files, x.prog.Wrote)
	}

	return x.cleanup(files)
}

// ar files are not compressed.
func getUncompressedArSize(arFile io.ReadCloser) (total, compressed uint64, count int) {
	defer arFile.Close()

	arReader := ar.NewReader(arFile)

	for {
		header, err := arReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, 0, count
			}

			return total, 0, count
		}

		total += uint64(header.Size)
		count++
	}
}
