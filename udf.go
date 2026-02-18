package xtractr

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golift.io/udf"
)

// isUDFCandidate returns true for errors that suggest the image might be UDF.
func isUDFCandidate(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "BEA01") || strings.Contains(msg, "UDF")
}

// extractUDF extracts a UDF volume image to disk.
func extractUDF(xFile *XFile, ra io.ReaderAt) (uint64, []string, error) {
	udfImage, err := udf.NewUdfFromReader(ra)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open UDF image: %s: %w", xFile.FilePath, err)
	}

	defer xFile.newProgress(getUncompressedUDFSize(udfImage)).done()

	size, files, err := xFile.unUDF(udfImage, nil, "")
	if err != nil {
		return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	return size, files, nil
}

// getUncompressedUDFSize calculates the total size of all files in a UDF volume.
//

func getUncompressedUDFSize(udfImage *udf.Udf) (total, _ uint64, count int) {
	var walk func(fe *udf.FileEntry)

	walk = func(fe *udf.FileEntry) {
		files, err := udfImage.ReadDir(fe)
		if err != nil {
			return
		}

		for idx := range files {
			count++

			if files[idx].IsDir() {
				fe, err := files[idx].FileEntry()
				if err != nil {
					continue
				}

				walk(fe)
			} else {
				total += uint64(files[idx].Size())
			}
		}
	}

	walk(nil)

	return total, 0, count
}

func (x *XFile) unUDF(udfImage *udf.Udf, fe *udf.FileEntry, parent string) (uint64, []string, error) {
	var files []string

	var totalSize uint64

	entries, err := udfImage.ReadDir(fe)
	if err != nil {
		return 0, nil, fmt.Errorf("reading UDF directory: %w", err)
	}

	for i := range entries {
		size, entryFiles, err := x.unUDFEntry(udfImage, &entries[i], parent)
		totalSize += size

		files = append(files, entryFiles...)

		if err != nil {
			return totalSize, files, err
		}
	}

	return totalSize, files, nil
}

func (x *XFile) unUDFEntry(udfImage *udf.Udf, entry *udf.File, parent string) (uint64, []string, error) {
	if entry.IsDir() {
		return x.unUDFDir(udfImage, entry, parent)
	}

	return x.unUDFFile(entry, parent)
}

func (x *XFile) unUDFDir(udfImage *udf.Udf, entry *udf.File, parent string) (uint64, []string, error) {
	dirPath := filepath.Join(parent, entry.Name())

	err := x.mkDir(filepath.Join(x.OutputDir, dirPath), entry.Mode(), entry.ModTime())
	if err != nil {
		return 0, nil, fmt.Errorf("making UDF directory %s: %w", entry.Name(), err)
	}

	entryFE, err := entry.FileEntry()
	if err != nil {
		return 0, nil, fmt.Errorf("reading UDF file entry for %s: %w", entry.Name(), err)
	}

	return x.unUDF(udfImage, entryFE, dirPath)
}

func (x *XFile) unUDFFile(entry *udf.File, parent string) (uint64, []string, error) {
	filePath := filepath.Join(parent, entry.Name())

	reader, err := entry.NewReader()
	if err != nil {
		return 0, nil, fmt.Errorf("creating reader for UDF file %s: %w", entry.Name(), err)
	}

	output := &file{
		Path:     x.clean(filePath),
		Data:     reader,
		FileMode: entry.Mode(),
		DirMode:  x.DirMode,
		Mtime:    entry.ModTime(),
	}

	//nolint:gocritic
	if !strings.HasPrefix(output.Path, filepath.Join(x.OutputDir)) {
		return 0, nil, fmt.Errorf("%s: %w: %s != %s (from: %s)",
			x.FilePath, ErrInvalidPath, output.Path, x.OutputDir, entry.Name())
	}

	x.Debugf("Writing UDF file: %s (bytes: %d)", output.Path, entry.Size())

	size, err := x.write(output)
	if err != nil {
		return 0, nil, fmt.Errorf("writing UDF file %s: %w", entry.Name(), err)
	}

	return size, []string{output.Path}, nil
}
