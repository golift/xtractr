package xtractr

/* How to extract a RAR file. */

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nwaples/rardecode/v2"
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
		// Copy the input so the retry keeps the logger, progress callbacks,
		// SquashRoot and the rest of the caller-provided configuration.
		attempt := *xFile
		attempt.Password = password

		size, files, archives, err := extractRAR(&attempt)
		xFile.prog = attempt.prog

		switch {
		case err == nil:
			return size, files, archives, nil
		case isLimitError(err):
			return size, files, archives, err
		case strings.Contains(err.Error(), "incorrect password"):
			// https://github.com/nwaples/rardecode/issues/28
			continue
		default:
			return size, files, archives, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
		}
	}

	// No password worked, try without a password.
	attempt := *xFile
	attempt.Password = ""

	return extractRAR(&attempt)
}

// extractRAR extracts a rar file. to a destination. This wraps github.com/nwaples/rardecode.
func extractRAR(xFile *XFile) (uint64, []string, []string, error) {
	rarReader, err := rardecode.OpenReader(xFile.FilePath, rardecode.Password(xFile.Password))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}

	tracker, headerErr := xFile.archiveProgress(getUncompressedRarSize(rarReader, xFile.FilePath))
	defer tracker.done() // getUncompressedRarSize closed rarReader

	if headerErr != nil {
		return 0, nil, nil, headerErr
	}

	rarReader, err = rardecode.OpenReader(xFile.FilePath, rardecode.Password(xFile.Password)) // open it again.
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	files, err := xFile.unrar(rarReader)
	if err != nil {
		volumes := normalizeVolumes(rarReader.Volumes(), xFile.FilePath)
		lastFile := volumes[len(volumes)-1]

		return xFile.prog.Wrote, files, volumes, fmt.Errorf("%s: %w", lastFile, err)
	}

	return xFile.prog.Wrote, files, normalizeVolumes(rarReader.Volumes(), xFile.FilePath), nil
}

func getUncompressedRarSize(rarReader *rardecode.ReadCloser, filePath string) (total, compressed uint64, count int) {
	defer rarReader.Close()

	for {
		header, err := rarReader.Next()
		if err != nil {
			compressed = archiveFileSizes(normalizeVolumes(rarReader.Volumes(), filePath)...)

			return total, compressed, count
		}

		total += uint64(header.UnPackedSize)
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
			Linkname: header.LinkTarget,
		}

		// RAR5 stores symlink targets in a redirection record (not file payload).
		// Mode() already flags unix/windows symlinks; junctions still need ModeSymlink.
		switch header.LinkType {
		case rardecode.LinkTypeUnixSymlink, rardecode.LinkTypeWindowsSymlink, rardecode.LinkTypeWindowsJunction:
			file.FileMode |= os.ModeSymlink
		}

		if !x.pathWithinOutput(file.Path) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return files, fmt.Errorf("%s: %w: %s != %s (from: %s)",
				x.FilePath, ErrInvalidPath, file.Path, x.OutputDir, header.Name)
		}

		if header.IsDir {
			x.Debugf("Writing archived directory: %s", file.Path)

			err = x.mkDir(file.Path, header.Mode(), header.ModificationTime)
			if err != nil {
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
