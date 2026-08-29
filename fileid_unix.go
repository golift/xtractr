//go:build unix

package xtractr

import (
	"fmt"
	"os"
	"syscall"
)

// identifyFile follows the path (so symlink aliases share an identity) and
// returns the device and inode of the target.
func identifyFile(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}

	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), true
}
