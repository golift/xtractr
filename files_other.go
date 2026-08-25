//go:build !unix && !windows

package xtractr

import (
	"fmt"
	"os"
)

// openFileNoFollow falls back to os.OpenFile on platforms without a no-follow open.
// A pre-existing final-component symlink is refused; a symlink planted after this
// Lstat can still be followed on these platforms.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, errExtractSymlink
	}

	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return nil, fmt.Errorf("os.OpenFile(): %w", err)
	}

	return file, nil
}

func requireDiskFile(*os.File) error {
	return nil
}
