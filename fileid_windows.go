//go:build windows

package xtractr

import (
	"fmt"
	"os"
	"syscall"
)

const fileIndexHighShift = 32

// identifyFile follows the path and returns the volume serial plus file index.
func identifyFile(path string) (string, bool) {
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()

	var info syscall.ByHandleFileInformation

	err = syscall.GetFileInformationByHandle(syscall.Handle(file.Fd()), &info)
	if err != nil {
		return "", false
	}

	ino := uint64(info.FileIndexHigh)<<fileIndexHighShift | uint64(info.FileIndexLow)

	return fmt.Sprintf("%d:%d", info.VolumeSerialNumber, ino), true
}
