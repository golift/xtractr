//go:build windows

package xtractr

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// fsctlSetSparse is FSCTL_SET_SPARSE. NTFS writes a hole as real zeros until
// this is set on the file.
const fsctlSetSparse = 0x000900C4

var procDeviceIoControl = syscall.NewLazyDLL("kernel32.dll").NewProc("DeviceIoControl") //nolint:gochecknoglobals

func markFileSparse(file *os.File) error {
	var returned uint32

	//nolint:gosec // DeviceIoControl takes a pointer for the returned byte count.
	result, _, err := procDeviceIoControl.Call(
		file.Fd(),
		uintptr(fsctlSetSparse),
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&returned)),
		0,
	)
	if result == 0 {
		return fmt.Errorf("marking file sparse: %w", err)
	}

	return nil
}
