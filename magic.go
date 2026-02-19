package xtractr

/* Code to detect archive types by file signatures (magic numbers). */

import (
	"bytes"
	"fmt"
	"os"
)

// signature maps a byte pattern at a specific offset to an extract function.
type signature struct {
	// Offset is the byte offset where the magic bytes are expected.
	Offset int
	// Magic is the byte sequence to match at Offset.
	Magic []byte
	// Extract function for this signature.
	Extract Interface
}

// maxSignatureRead is the maximum number of bytes to read for signature detection.
// This is enough for ISO9660 detection at offset 0x9001 + 5 bytes for "CD001".
const maxSignatureRead = 0x9006

// signatureTable maps file signatures (magic numbers) to their corresponding extract functions.
//
//nolint:gochecknoglobals
var signatureTable = []signature{
	// RAR v5 (longer match first).
	{Offset: 0, Magic: []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}, Extract: ExtractRAR},
	// RAR v4.
	{Offset: 0, Magic: []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}, Extract: ExtractRAR},
	// 7-Zip.
	{Offset: 0, Magic: []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}, Extract: Extract7z},
	// ZIP (PK\x03\x04).
	{Offset: 0, Magic: []byte{0x50, 0x4B, 0x03, 0x04}, Extract: ChngInt(ExtractZIP)},
	// Gzip.
	{Offset: 0, Magic: []byte{0x1F, 0x8B}, Extract: ChngInt(ExtractGzip)},
	// Bzip2 (BZh).
	{Offset: 0, Magic: []byte{0x42, 0x5A, 0x68}, Extract: ChngInt(ExtractBzip)},
	// XZ.
	{Offset: 0, Magic: []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}, Extract: ChngInt(ExtractXZ)},
	// Zstandard.
	{Offset: 0, Magic: []byte{0x28, 0xB5, 0x2F, 0xFD}, Extract: ChngInt(ExtractZstandard)},
	// LZ4.
	{Offset: 0, Magic: []byte{0x04, 0x22, 0x4D, 0x18}, Extract: ChngInt(ExtractLZ4)},
	// LZMA.
	{Offset: 0, Magic: []byte{0x5D, 0x00, 0x00}, Extract: ChngInt(ExtractLZMA)},
	// Brotli.
	{Offset: 0, Magic: []byte{0xCE, 0xB2, 0xCF, 0x81}, Extract: ChngInt(ExtractBrotli)},
	// AR / DEB ("!<arch>\n").
	{Offset: 0, Magic: []byte{0x21, 0x3C, 0x61, 0x72, 0x63, 0x68, 0x3E, 0x0A}, Extract: ChngInt(ExtractAr)},
	// RPM.
	{Offset: 0, Magic: []byte{0xED, 0xAB, 0xEE, 0xDB}, Extract: ChngInt(ExtractRPM)},
	// ISO9660 at offset 0x8001.
	{Offset: 0x8001, Magic: []byte{0x43, 0x44, 0x30, 0x30, 0x31}, Extract: ChngInt(ExtractISO)}, //nolint:mnd
	// ISO9660 at offset 0x8801.
	{Offset: 0x8801, Magic: []byte{0x43, 0x44, 0x30, 0x30, 0x31}, Extract: ChngInt(ExtractISO)}, //nolint:mnd
	// ISO9660 at offset 0x9001.
	{Offset: 0x9001, Magic: []byte{0x43, 0x44, 0x30, 0x30, 0x31}, Extract: ChngInt(ExtractISO)}, //nolint:mnd
}

// detectBySignature reads the first bytes of a file and attempts to match
// a known file signature (magic number) to determine the archive type.
func detectBySignature(filePath string) (Interface, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening file for signature detection: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file for signature detection: %w", err)
	}

	readSize := min(stat.Size(), int64(maxSignatureRead))

	buf := make([]byte, readSize)

	n, err := file.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("reading file for signature detection: %w", err)
	}

	buf = buf[:n]

	for _, sig := range signatureTable {
		end := sig.Offset + len(sig.Magic)
		if end > len(buf) {
			continue
		}

		if bytes.Equal(buf[sig.Offset:end], sig.Magic) {
			return sig.Extract, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrUnknownArchiveType, filePath)
}

// IsArchiveFileByContent returns true if the provided file path contains
// a recognized archive file signature. Unlike IsArchiveFile, this reads
// the actual file content rather than relying on the file extension.
func IsArchiveFileByContent(path string) bool {
	extractFn, err := detectBySignature(path)
	return err == nil && extractFn != nil
}
