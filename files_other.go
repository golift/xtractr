//go:build !unix && !windows

package xtractr

import (
	"fmt"
	"os"
)

// openFileNoFollow falls back to os.OpenFile on platforms without a no-follow open.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	file, err := os.OpenFile(path, flags, mode)
	if err != nil {
		return nil, fmt.Errorf("os.OpenFile(): %w", err)
	}

	return file, nil
}
