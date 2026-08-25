//go:build windows

package xtractr

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// FILE_FLAG_OPEN_REPARSE_POINT: open the reparse point itself, do not follow it.
const fileFlagOpenReparsePoint = 0x00200000

// openFileNoFollow opens path for extract writes without following a final-component
// symlink. A reparse point is reported as errExtractSymlink so the caller can
// replace it with O_EXCL rather than writing through it.
//
// OPEN_EXISTING is used instead of CREATE_ALWAYS so a planted reparse point is
// inspected rather than replaced-or-followed before we can refuse it.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	handle, createdNew, err := createNoFollowHandle(path, flags)
	if err != nil {
		return nil, err
	}

	if !createdNew {
		err = refuseReparsePoint(handle)
		if err != nil {
			_ = syscall.CloseHandle(handle)

			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
	}

	if flags&os.O_TRUNC != 0 {
		err = syscall.SetEndOfFile(handle)
		if err != nil {
			_ = syscall.CloseHandle(handle)

			return nil, &os.PathError{Op: "truncate", Path: path, Err: err}
		}
	}

	file := os.NewFile(uintptr(handle), path)
	_ = file.Chmod(mode)

	return file, nil
}

func createNoFollowHandle(path string, flags int) (syscall.Handle, bool, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, false, fmt.Errorf("path: %w", err)
	}

	access := uint32(syscall.GENERIC_READ | syscall.GENERIC_WRITE)
	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	attrs := uint32(syscall.FILE_ATTRIBUTE_NORMAL) | fileFlagOpenReparsePoint
	createdNew := flags&os.O_EXCL != 0
	createmode := uint32(syscall.OPEN_EXISTING)

	if createdNew {
		createmode = syscall.CREATE_NEW
	}

	handle, err := syscall.CreateFile(pathp, access, share, nil, createmode, attrs, 0)
	if err != nil && !createdNew && flags&os.O_CREATE != 0 && errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		handle, err = syscall.CreateFile(pathp, access, share, nil, syscall.CREATE_NEW, attrs, 0)
		createdNew = true
	}

	if err != nil {
		return syscall.InvalidHandle, false, &os.PathError{Op: "open", Path: path, Err: err}
	}

	return handle, createdNew, nil
}

func refuseReparsePoint(handle syscall.Handle) error {
	var info syscall.ByHandleFileInformation

	err := syscall.GetFileInformationByHandle(handle, &info)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}

	if info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errExtractSymlink
	}

	return nil
}
