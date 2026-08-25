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
// unlink it and retry with CREATE_NEW rather than writing through it.
//
// CREATE_NEW (O_EXCL) or OPEN_EXISTING (everything else). There is no
// CREATE_ALWAYS / OPEN_ALWAYS fallback: those can follow or smash a reparse
// point before we inspect it. A missing-file race is retried by openExtractFile.
//
// FILE_FLAG_BACKUP_SEMANTICS is required to open a directory or directory
// junction; without it OPEN_EXISTING fails before we can classify a planted
// reparse point and replace it.
func openFileNoFollow(path string, flags int, mode os.FileMode) (*os.File, error) {
	handle, createdNew, err := createNoFollowHandle(path, flags)
	if err != nil {
		return nil, err
	}

	err = inspectExtractHandle(handle, createdNew)
	if err != nil {
		_ = syscall.CloseHandle(handle)

		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}

	file := os.NewFile(uintptr(handle), path)
	if createdNew {
		_ = file.Chmod(mode)
	}

	return file, nil
}

func createNoFollowHandle(path string, flags int) (syscall.Handle, bool, error) {
	pathp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return syscall.InvalidHandle, false, fmt.Errorf("path: %w", err)
	}

	share := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	attrs := uint32(syscall.FILE_ATTRIBUTE_NORMAL) | fileFlagOpenReparsePoint | syscall.FILE_FLAG_BACKUP_SEMANTICS
	createdNew := flags&os.O_EXCL != 0
	createmode := uint32(syscall.OPEN_EXISTING)
	access := uint32(syscall.GENERIC_READ | syscall.GENERIC_WRITE)

	if createdNew {
		createmode = syscall.CREATE_NEW
	}

	handle, err := syscall.CreateFile(pathp, access, share, nil, createmode, attrs, 0)
	if err != nil && !createdNew && errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		handle, err = syscall.CreateFile(pathp, syscall.GENERIC_READ, share, nil, createmode, attrs, 0)
	}

	if err != nil {
		return syscall.InvalidHandle, false, &os.PathError{Op: "open", Path: path, Err: err}
	}

	return handle, createdNew, nil
}

// inspectExtractHandle rejects devices/pipes before querying reparse attributes.
// GetFileInformationByHandle is unsupported for NUL/CON/pipes and would hide
// errExtractNotRegular behind a generic stat error.
func inspectExtractHandle(handle syscall.Handle, createdNew bool) error {
	err := refuseNonDiskHandle(handle)
	if err != nil {
		return err
	}

	if createdNew {
		return nil
	}

	return refuseReparsePoint(handle)
}

func refuseNonDiskHandle(handle syscall.Handle) error {
	kind, err := syscall.GetFileType(handle)
	if err != nil {
		return fmt.Errorf("file type: %w", err)
	}

	if kind != syscall.FILE_TYPE_DISK {
		return errExtractNotRegular
	}

	return nil
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

func isDeniedExclusiveCreate(err error) bool {
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED)
}

// requireDiskFile rejects Windows devices and pipes (NUL, CON, COM1, named pipes).
// os.File.Stat on those handles can look like a regular file.
func requireDiskFile(file *os.File) error {
	return refuseNonDiskHandle(syscall.Handle(file.Fd()))
}
