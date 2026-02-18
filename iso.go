package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
	"golift.io/udf"
)

// ExtractISO writes an ISO's contents to disk.
// If the image is not a valid ISO9660 volume (e.g. UDF-only), it falls back
// to the UDF reader automatically.
func ExtractISO(xFile *XFile) (size uint64, filesList []string, err error) {
	openISO, err := os.Open(xFile.FilePath) // os.Open on purpose.
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer openISO.Close()

	image, isoErr := iso9660.OpenImage(openISO)

	// If ISO9660 parsing fails with UDF error or other issues, try UDF.
	if isoErr != nil {
		if errors.Is(isoErr, iso9660.ErrUDFNotSupported) || isUDFCandidate(isoErr) {
			return extractUDF(xFile, openISO)
		}
		return 0, nil, fmt.Errorf("failed to open iso image: %s: %w", xFile.FilePath, isoErr)
	}

	defer xFile.newProgress(getUncompressedIsoSize(image)).done()

	iso, err := iso9660.OpenImage(xFile.prog.readAter(openISO))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso image: %s: %w", xFile.FilePath, err)
	}

	root, err := iso.RootDir()
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso root: %s: %w", xFile.FilePath, err)
	}

	// Extract directly to output directory (no ISO-name subfolder).
	size, files, err := xFile.uniso(root, "")
	if err != nil {
		return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	return size, files, nil
}

// isUDFCandidate returns true for errors that suggest the image might be UDF.
func isUDFCandidate(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "BEA01") || strings.Contains(msg, "UDF")
}

//nolint:unparam // so we can pass it in.
func getUncompressedIsoSize(image *iso9660.Image) (total, _ uint64, count int) {
	if image == nil {
		return total, 0, count
	}

	var loop func(isoFile *iso9660.File)

	loop = func(isoFile *iso9660.File) {
		count++

		children, err := isoFile.GetChildren()
		if err != nil {
			return
		}

		for _, child := range children {
			total += uint64(child.Size())
			loop(child)
		}
	}

	root, err := image.RootDir()
	if err != nil {
		return total, 0, count
	}

	loop(root)

	return total, 0, count
}

func (x *XFile) uniso(isoFile *iso9660.File, parent string) (uint64, []string, error) {
	itemName := filepath.Join(parent, isoFile.Name())

	if isoFile.Name() == string([]byte{0}) { // root directory - extract to output dir directly.
		itemName = ""
	}

	if !isoFile.IsDir() { // it's a file
		return x.unisofile(isoFile, itemName)
	}

	if itemName != "" {
		err := x.mkDir(filepath.Join(x.OutputDir, itemName), isoFile.Mode(), isoFile.ModTime())
		if err != nil {
			return 0, nil, fmt.Errorf("making iso directory %s: %w", isoFile.Name(), err)
		}
	}

	children, err := isoFile.GetChildren()
	if err != nil {
		return 0, nil, fmt.Errorf("getting children for %s: %w", isoFile.Name(), err)
	}

	files := []string{}
	size := uint64(0)

	for _, child := range children {
		childSize, childFiles, err := x.uniso(child, itemName)
		if err != nil {
			return size + childSize, files, err
		}

		size += childSize

		files = append(files, childFiles...)
	}

	files, err = x.cleanup(files)

	return size, files, err
}

func (x *XFile) unisofile(isoFile *iso9660.File, wfile string) (uint64, []string, error) {
	file := &file{
		Path:     x.clean(wfile),
		Data:     isoFile.Reader(),
		FileMode: isoFile.Mode(),
		DirMode:  x.DirMode,
		Mtime:    isoFile.ModTime(),
	}

	//nolint:gocritic // this 1-argument filepath.Join removes a ./ prefix should there be one.
	if !strings.HasPrefix(file.Path, filepath.Join(x.OutputDir)) {
		// The file being written is trying to write outside of our base path. Malicious ISO?
		return 0, nil, fmt.Errorf("%s: %w: %s != %s (from: %s)",
			x.FilePath, ErrInvalidPath, file.Path, x.OutputDir, isoFile.Name())
	}

	x.Debugf("Writing archived file: %s (bytes: %d)", file.Path, isoFile.Size())

	size, err := x.write(file)
	x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
		file.Path, size, x.prog.Files, int64(x.prog.Wrote))

	return size, []string{file.Path}, err
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
//nolint:unparam
func getUncompressedUDFSize(udfImage *udf.Udf) (total, _ uint64, count int) {
	var walk func(fe *udf.FileEntry)

	walk = func(fe *udf.FileEntry) {
		files, err := udfImage.ReadDir(fe)
		if err != nil {
			return
		}

		for i := range files {
			count++

			if files[i].IsDir() {
				fe, err := files[i].FileEntry()
				if err != nil {
					continue
				}

				walk(fe)
			} else {
				total += uint64(files[i].Size())
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
		entry := &entries[i]

		if entry.IsDir() {
			dirPath := filepath.Join(parent, entry.Name())

			err := x.mkDir(filepath.Join(x.OutputDir, dirPath), entry.Mode(), entry.ModTime())
			if err != nil {
				return totalSize, files, fmt.Errorf("making UDF directory %s: %w", entry.Name(), err)
			}

			entryFE, err := entry.FileEntry()
			if err != nil {
				return totalSize, files, fmt.Errorf("reading UDF file entry for %s: %w", entry.Name(), err)
			}

			childSize, childFiles, err := x.unUDF(udfImage, entryFE, dirPath)
			if err != nil {
				return totalSize + childSize, files, err
			}

			totalSize += childSize
			files = append(files, childFiles...)
		} else {
			filePath := filepath.Join(parent, entry.Name())

			reader, err := entry.NewReader()
			if err != nil {
				return totalSize, files, fmt.Errorf("creating reader for UDF file %s: %w", entry.Name(), err)
			}

			f := &file{
				Path:     x.clean(filePath),
				Data:     reader,
				FileMode: entry.Mode(),
				DirMode:  x.DirMode,
				Mtime:    entry.ModTime(),
			}

			//nolint:gocritic
			if !strings.HasPrefix(f.Path, filepath.Join(x.OutputDir)) {
				return totalSize, files, fmt.Errorf("%s: %w: %s != %s (from: %s)",
					x.FilePath, ErrInvalidPath, f.Path, x.OutputDir, entry.Name())
			}

			x.Debugf("Writing UDF file: %s (bytes: %d)", f.Path, entry.Size())

			size, err := x.write(f)
			if err != nil {
				return totalSize, files, fmt.Errorf("writing UDF file %s: %w", entry.Name(), err)
			}

			totalSize += size
			files = append(files, f.Path)
		}
	}

	return totalSize, files, nil
}
