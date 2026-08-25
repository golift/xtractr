//go:build !unix && !windows

package xtractr

import (
	"fmt"
	"os"
)

// openFileNoFollow cannot implement a true no-follow open on this platform.
// Exclusive-create (O_EXCL) does not follow a final-component symlink.
// Any other open is refused so the caller unlinks the name and retries with
// O_EXCL, instead of os.OpenFile-following a symlink planted after Lstat.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	if flags&os.O_EXCL == 0 {
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
