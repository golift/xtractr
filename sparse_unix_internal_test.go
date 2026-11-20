//go:build unix

package xtractr

import (
	"os"
	"syscall"
)

func allocatedBytes(path string) (int64, bool) {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return 0, false
	}
	defer file.Close()

	// Delayed allocation reports a hole as fully allocated until the file is synced.
	_ = file.Sync()

	var info syscall.Stat_t

	err = syscall.Fstat(int(file.Fd()), &info)
	if err != nil {
		return 0, false
	}

	return info.Blocks * 512, true
}
