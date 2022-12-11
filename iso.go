package xtractr

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kdomanski/iso9660"
)

// ExtractISO writes an ISO's contents to disk.
func ExtractISO(xFile *XFile) (int64, []string, error) {
	openISO, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to open iso: %s: %w", xFile.FilePath, err)
	}
	defer openISO.Close()

	iso, err := iso9660.OpenImage(openISO)
	if err != nil {
		return 0, nil, err
	}

	root, err := iso.RootDir()
	if err != nil {
		return 0, nil, err
	}

	size, files, err := xFile.uniso(root, xFile.OutputDir)
	if err != nil {
		return size, files, fmt.Errorf("%s: %w", xFile.FilePath, err)
	}

	return size, files, nil
}

func (x *XFile) uniso(isoFile *iso9660.File, dest string) (int64, []string, error) {
	wfile := x.clean(isoFile.Name())
	// nolint:gocritic // this 1-argument filepath.Join removes a ./ prefix should there be one.
	if !strings.HasPrefix(wfile, filepath.Join(x.OutputDir)) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, nil, fmt.Errorf("%s: %w: %s != %s (from: %s)",
			x.FilePath, ErrInvalidPath, wfile, x.OutputDir, isoFile.Name())
	}

	if !isoFile.IsDir() { // it's a file
		newFile, err := os.Create(dest)
		if err != nil {
			return 0, nil, err
		}
		defer newFile.Close()

		size, err := io.Copy(newFile, isoFile.Reader())
		if err != nil {
			return size, []string{dest}, err
		}

		return size, []string{dest}, err
	}

	if err := os.Mkdir(dest, x.DirMode); err != nil {
		return 0, nil, err
	}

	children, err := isoFile.GetChildren()
	if err != nil {
		return 0, nil, err
	}

	files := []string{}
	size := int64(0)

	for _, child := range children {
		childSize, childFiles, err := x.uniso(child, filepath.Join(dest, child.Name()))
		if err != nil {
			return size, files, err
		}

		size += childSize
		files = append(files, childFiles...)
	}

	return size, files, nil
}
