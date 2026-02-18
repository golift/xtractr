package xtractr

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

/* How to extract a ZIP file. */

// ExtractZIP extracts a zip file.. to a destination. Simple enough.
func ExtractZIP(xFile *XFile) (size uint64, filesList []string, err error) {
	zipReader, err := zip.OpenReader(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("zip.OpenReader: %w", err)
	}
	defer zipReader.Close()

	defer xFile.newProgress(getUncompressedZipSize(zipReader)).done()

	// Detect encoding for non-UTF8 filenames in the archive.
	decoder := detectZipEncoding(xFile, zipReader.File)

	files := []string{}

	for _, zipFile := range zipReader.File {
		decodedName := decodeZipFilename(zipFile.Name, zipFile.NonUTF8, decoder)

		fSize, wfile, err := xFile.unzipWithName(zipFile, decodedName)
		if err != nil {
			return xFile.prog.Wrote, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
		}

		files = append(files, filepath.Join(xFile.OutputDir, decodedName))
		xFile.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			wfile, fSize, xFile.prog.Files, xFile.prog.Wrote)
	}

	files, err = xFile.cleanup(files)

	return xFile.prog.Wrote, files, err
}

func getUncompressedZipSize(zipReader *zip.ReadCloser) (total, compressed uint64, count int) {
	for _, zipFile := range zipReader.File {
		total += zipFile.UncompressedSize64
		// compressed += zipFile.CompressedSize64
		count++
	}

	return total, 0, count
}

func (x *XFile) unzipWithName(zipFile *zip.File, name string) (uint64, string, error) {
	zFile, err := zipFile.Open()
	if err != nil {
		return 0, name, fmt.Errorf("zipFile.Open: %w", err)
	}
	defer zFile.Close()

	file := &file{
		Path:     x.clean(name),
		Data:     zFile,
		FileMode: zipFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    zipFile.Modified,
		Atime:    time.Now(),
	}

	if !strings.HasPrefix(file.Path, x.OutputDir) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		err := fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), ErrInvalidPath, file.Path, name)
		return 0, file.Path, err
	}

	if zipFile.FileInfo().IsDir() {
		x.Debugf("Writing archived directory: %s", file.Path)

		err := x.mkDir(file.Path, zipFile.Mode(), zipFile.Modified)
		if err != nil {
			return 0, file.Path, fmt.Errorf("making zipFile dir: %w", err)
		}

		return 0, file.Path, nil
	}

	x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)", file.Path,
		zipFile.CompressedSize64, zipFile.UncompressedSize64)

	s, err := x.write(file)
	if err != nil {
		return s, file.Path, fmt.Errorf("%s: %w: %s (from: %s)", zipFile.FileInfo().Name(), err, file.Path, name)
	}

	return s, file.Path, nil
}
