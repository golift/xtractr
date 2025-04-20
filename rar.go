package xtractr

/* How to extract a RAR file. */

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode"
)

// ExtractRAR attempts to extract a file as a rar file.
func ExtractRAR(xFile *XFile) (size uint64, filesList, archiveList []string, err error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		return extractRAR(xFile)
	}

	// Try all the passwords.
	passwords := xFile.Passwords

	if xFile.Password != "" { // If a single password is provided, try it first.
		passwords = append([]string{xFile.Password}, xFile.Passwords...)
	}

	for idx, password := range passwords {
		size, files, archives, err := extractRAR(&XFile{
			FilePath:  xFile.FilePath,
			OutputDir: xFile.OutputDir,
			FileMode:  xFile.FileMode,
			DirMode:   xFile.DirMode,
			Password:  password,
		})
		if err == nil {
			return size, files, archives, nil
		}

		// https://github.com/nwaples/rardecode/issues/28
		if strings.Contains(err.Error(), "incorrect password") {
			continue
		}

		return size, files, archives, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
	}

	// No password worked, try without a password.
	return extractRAR(&XFile{
		FilePath:  xFile.FilePath,
		OutputDir: xFile.OutputDir,
		FileMode:  xFile.FileMode,
		DirMode:   xFile.DirMode,
	})
}

// extractRAR extracts a rar file. to a destination. This wraps github.com/nwaples/rardecode.
func extractRAR(xFile *XFile) (uint64, []string, []string, error) {
	rarReader, err := rardecode.OpenReader(xFile.FilePath, xFile.Password)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}

	defer xFile.newProgress(getUncompressedRarSize(rarReader)).done() // this closes rarReader

	rarReader, err = rardecode.OpenReader(xFile.FilePath, xFile.Password) // open it again.
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	files, err := xFile.unrar(rarReader)
	if err != nil {
		lastFile := xFile.FilePath
		if volumes := rarReader.Volumes(); len(volumes) > 0 {
			lastFile = volumes[len(volumes)-1]
		}

		return xFile.prog.Wrote, files, rarReader.Volumes(), fmt.Errorf("%s: %w", lastFile, err)
	}

	return xFile.prog.Wrote, files, rarReader.Volumes(), nil
}

func getUncompressedRarSize(rarReader *rardecode.ReadCloser) (total, compressed uint64, count int) {
	defer rarReader.Close()

	for {
		header, err := rarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, 0, count
			}

			return total, 0, count
		}

		total += uint64(header.UnPackedSize)
		// compressed += uint64(header.PackedSize)
		count++
	}
}

func (x *XFile) unrar(rarReader *rardecode.ReadCloser) ([]string, error) {
	files := []string{}

	for {
		header, err := rarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return files, fmt.Errorf("rarReader.Next: %w", err)
		}

		file := &file{
			Path:     x.clean(header.Name),
			Data:     rarReader,
			FileMode: header.Mode(),
			DirMode:  x.DirMode,
			Mtime:    header.ModificationTime,
			Atime:    header.AccessTime,
		}
		//nolint:gocritic // this 1-argument filepath.Join removes a ./ prefix should there be one.
		if !strings.HasPrefix(file.Path, filepath.Join(x.OutputDir)) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return files, fmt.Errorf("%s: %w: %s != %s (from: %s)",
				x.FilePath, ErrInvalidPath, file.Path, x.OutputDir, header.Name)
		}

		if header.IsDir {
			x.Debugf("Writing archived directory: %s", file.Path)

			if err = x.mkDir(file.Path, header.Mode(), header.ModificationTime); err != nil {
				return files, fmt.Errorf("making rar file dir: %w", err)
			}

			continue
		}

		x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)",
			file.Path, header.PackedSize, header.UnPackedSize)

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
