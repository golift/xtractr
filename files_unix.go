//go:build unix

package xtractr

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openFileNoFollow is os.OpenFile plus O_NOFOLLOW so a final-component symlink
// fails with ELOOP instead of writing through to the target.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW, mode)
	if err == nil {
		return file, nil
	}

	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("%w: %w", errExtractSymlink, err)
	}

	return nil, fmt.Errorf("os.OpenFile(): %w", err)
}
