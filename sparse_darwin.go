//go:build darwin

package xtractr

import (
	"os"
	"syscall"
	"unsafe"
)

// fPunchHole is F_PUNCHHOLE. A seek past the end does not deallocate on APFS;
// this does, and the bytes still read back as zeros.
const fPunchHole = 99

// punchAlign is the APFS allocation block. F_PUNCHHOLE rejects a range that
// is not a multiple of it.
const punchAlign int64 = 4096

type filePunchHole struct {
	flags    uint32
	reserved uint32
	offset   int64
	length   int64
}

func punchFileHole(file *os.File, offset, length int64) {
	if file == nil || length <= 0 {
		return
	}

	start := (offset + punchAlign - 1) &^ (punchAlign - 1)
	end := (offset + length) &^ (punchAlign - 1)

	if end <= start {
		return
	}

	hole := filePunchHole{offset: start, length: end - start}

	//nolint:gosec // F_PUNCHHOLE takes a pointer to the range.
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), fPunchHole, uintptr(unsafe.Pointer(&hole)))

	// The range already reads as zeros. A failed punch only means it stays
	// allocated, so the extract still succeeds.
	_ = errno
}
