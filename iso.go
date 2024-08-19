package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
)

// ExtractISO writes an ISO's contents to disk.
func ExtractISO(xFile *XFile) (int64, []string, error) {
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

func (x *XFile) unisofile(isoFile *iso9660.File, fileName string) (int64, []string, error) {
	destFile := x.clean(fileName)
	//nolint:gocritic // this 1-argument filepath.Clean removes a ./ prefix should there be one.
	if !strings.HasPrefix(destFile, filepath.Clean(x.OutputDir)) {
		// The file being written is trying to write outside of our base path. Malicious ISO?
		return 0, nil, fmt.Errorf("%s: %w: %s != %s (from: %s)",
			x.FilePath, ErrInvalidPath, destFile, x.OutputDir, isoFile.Name())
	}

	size, err := writeFile(destFile, isoFile.Reader(), x.FileMode, x.DirMode)

	return size, []string{destFile}, err
}
