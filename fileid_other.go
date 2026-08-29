//go:build !unix && !windows

package xtractr

import (
	"path/filepath"
)

// identifyFile falls back to the resolved path when inode data is unavailable.
func identifyFile(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		resolved = filepath.Clean(path)
	}

	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", false
	}

	return abs, true
}
