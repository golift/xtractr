package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
)

// ExtractISO writes an ISO's contents to disk.
func ExtractISO(xFile *XFile) (size int64, filesList []string, err error) {
	openISO, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso file: %s: %w", xFile.FilePath, err)
	}
	defer openISO.Close()

	iso, err := iso9660.OpenImage(openISO)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso image: %s: %w", xFile.FilePath, err)
	}

	root, err := iso.RootDir()
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso root: %s: %w", xFile.FilePath, err)
	}

	size, files, err := xFile.uniso(root, "")
	if err != nil {
		return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	return size, files, nil
}

func (x *XFile) uniso(isoFile *iso9660.File, parent string) (int64, []string, error) {
	itemName := filepath.Join(parent, isoFile.Name())

	if isoFile.Name() == string([]byte{0}) { // rename root folder.
		itemName = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(x.FilePath), ".iso"), ".ISO")
	}

	if !isoFile.IsDir() { // it's a file
		return x.unisofile(isoFile, itemName)
	}

	if err := x.mkDir(filepath.Join(x.OutputDir, itemName), isoFile.Mode(), isoFile.ModTime()); err != nil {
		return 0, nil, fmt.Errorf("making iso directory %s: %w", isoFile.Name(), err)
	}

	children, err := isoFile.GetChildren()
	if err != nil {
		return 0, nil, fmt.Errorf("getting children for %s: %w", isoFile.Name(), err)
	}

	files := []string{}
	size := int64(0)

	for _, child := range children {
		childSize, childFiles, err := x.uniso(child, itemName)
		if err != nil {
			return size + childSize, files, err
		}

		size += childSize

		files = append(files, childFiles...)
	}

	return size, files, nil
}

func (x *XFile) unisofile(isoFile *iso9660.File, wfile string) (int64, []string, error) {
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

	return size, []string{file.Path}, err
}
