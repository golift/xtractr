package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
	"github.com/mogaika/udf"
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
	udfImage := udf.NewUdfFromReader(ra)

	// Use a deferred recover to catch panics from the UDF library,
	// which uses panic instead of returning errors.
	var panicErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = fmt.Errorf("UDF read error: %v", r)
			}
		}()
		// Force initialization by reading the root directory.
		udfImage.ReadDir(nil)
	}()

	if panicErr != nil {
		return 0, nil, fmt.Errorf("failed to open UDF image: %s: %w", xFile.FilePath, panicErr)
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
		files := udfImage.ReadDir(fe)
		for i := range files {
			count++
			if files[i].IsDir() {
				walk(files[i].FileEntry())
			} else {
				total += uint64(files[i].Size())
			}
		}
	}

	func() {
		defer func() { recover() }() //nolint:errcheck // UDF library panics on errors.
		walk(nil)
	}()

	return total, 0, count
}

func (x *XFile) unUDF(udfImage *udf.Udf, fe *udf.FileEntry, parent string) (uint64, []string, error) {
	var files []string
	var totalSize uint64

	// Recover from panics in the UDF library.
	var panicErr error

	func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = fmt.Errorf("UDF extraction error: %v", r)
			}
		}()

		entries := udfImage.ReadDir(fe)

		for i := range entries {
			entry := &entries[i]

			if entry.IsDir() {
				dirPath := filepath.Join(parent, entry.Name())
				dirMode := entry.Mode()
				modTime := entry.ModTime()

				err := x.mkDir(filepath.Join(x.OutputDir, dirPath), dirMode, modTime)
				if err != nil {
					panicErr = fmt.Errorf("making UDF directory %s: %w", entry.Name(), err)
					return
				}

				childSize, childFiles, err := x.unUDF(udfImage, entry.FileEntry(), dirPath)
				if err != nil {
					panicErr = err
					return
				}

				totalSize += childSize
				files = append(files, childFiles...)
			} else {
				filePath := filepath.Join(parent, entry.Name())
				f := &file{
					Path:     x.clean(filePath),
					Data:     entry.NewReader(),
					FileMode: entry.Mode(),
					DirMode:  x.DirMode,
					Mtime:    entry.ModTime(),
				}

				//nolint:gocritic
				if !strings.HasPrefix(f.Path, filepath.Join(x.OutputDir)) {
					panicErr = fmt.Errorf("%s: %w: %s != %s (from: %s)",
						x.FilePath, ErrInvalidPath, f.Path, x.OutputDir, entry.Name())
					return
				}

				x.Debugf("Writing UDF file: %s (bytes: %d)", f.Path, entry.Size())

				size, err := x.write(f)
				if err != nil {
					panicErr = fmt.Errorf("writing UDF file %s: %w", entry.Name(), err)
					return
				}

				totalSize += size
				files = append(files, f.Path)
			}
		}
	}()

	if panicErr != nil {
		return totalSize, files, panicErr
	}

	return totalSize, files, nil
}

