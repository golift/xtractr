package xtractr

import (
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
)

// closeNamed closes c and, if no error is already set, records the close error.
// Used with named return values so decoder checksums that surface on Close
// (gzip, zlib, zip members, 7z members) are not discarded by defer Close().
func closeNamed(closer io.Closer, err *error) { //nolint:gocritic // named returns require a pointer to error.
	if closer == nil || err == nil {
		return
	}

	closeErr := closer.Close()
	if closeErr != nil && *err == nil {
		*err = fmt.Errorf("closing: %w", closeErr)
	}
}

// checkCRC32 compares a running IEEE CRC against the value stored in an archive
// header. want 0 means the archive did not provide a checksum. On mismatch the
// written file is removed so a corrupt extract cannot look successful.
func checkCRC32(path string, got, want uint32) error {
	if want == 0 || got == want {
		return nil
	}

	_ = os.Remove(path)

	return fmt.Errorf("%s: %w (got %08x, want %08x)", path, ErrChecksum, got, want)
}

// teeCRC32 copies r into a CRC-32 IEEE hasher while the caller reads.
func teeCRC32(reader io.Reader) (io.Reader, hash.Hash32) {
	hasher := crc32.NewIEEE()

	return io.TeeReader(reader, hasher), hasher
}
