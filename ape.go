package xtractr

// APE (Monkey's Audio) binary-level splitting.
//
// Parses the APE container format (reverse-engineered from the Monkey's Audio
// source code: MAC/Source/MACLib/MACLib.h and APEHeader.cpp) and splits a
// single APE image file into individual per-track APE files by copying
// compressed frames verbatim — no audio decoding or re-encoding needed.

import (
	"bytes"
	"crypto/md5" //nolint:gosec // MD5 is mandated by the APE file format, not used for security.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// APE format constants from MAC/Source/MACLib/MACLib.h.
const (
	apeMinVersion          = 3980 // Only the "new" format (>= 3.98) is supported.
	apeDescriptorSize      = 52   // sizeof(APE_DESCRIPTOR)
	apeHeaderSize          = 24   // sizeof(APE_HEADER)
	apeFileMD5Size         = 16   // sizeof(APE_DESCRIPTOR.cFileMD5)
	apeFormatFlagCreateWAV = 1 << 5
	apeMaxMagicScan        = 1 << 20 // Scan up to 1 MB for the magic bytes.
	apeCopyBuf             = 1 << 20 // 1 MB copy buffer.

	bytesPerUint32 = 4          // APE seek-table entries and the bitstream word size.
	highDWordShift = 32         // Shift to combine the high/low halves of the 64-bit frame-data size.
	maxUint32      = 0xFFFFFFFF // Mask to keep the low 32 bits of a value.
	apeTailPadding = 4          // Extra bytes read past a realigned track to complete its final word.
)

// ID3v2 tag constants used to skip an optional tag before the APE descriptor.
const (
	id3v2HeaderLen  = 10   // ID3v2 tag header length (and footer length).
	id3v2FooterFlag = 0x10 // Bit in the flags byte indicating a 10-byte footer is present.
	id3v2SizeMask   = 0x7F // Each size byte is sync-safe: only the low 7 bits are used.
	id3v2SizeShift1 = 21
	id3v2SizeShift2 = 14
	id3v2SizeShift3 = 7
)

// apeDescriptor mirrors APE_DESCRIPTOR (MACLib.h:179-194), 52 bytes, little-endian.
type apeDescriptor struct {
	ID                 [4]byte
	Version            uint16
	Padding            uint16
	DescriptorBytes    uint32
	HeaderBytes        uint32
	SeekTableBytes     uint32
	HeaderDataBytes    uint32
	FrameDataBytes     uint32
	FrameDataBytesHigh uint32
	TerminatingBytes   uint32
	FileMD5            [16]byte
}

// apeHeader mirrors APE_HEADER (MACLib.h:199-211), 24 bytes, little-endian.
type apeHeader struct {
	CompressionLevel uint16
	FormatFlags      uint16
	BlocksPerFrame   uint32
	FinalFrameBlocks uint32
	TotalFrames      uint32
	BitsPerSample    uint16
	Channels         uint16
	SampleRate       uint32
}

// apeInfo holds all metadata needed to read and reconstruct APE files.
type apeInfo struct {
	Descriptor  apeDescriptor
	Header      apeHeader
	TotalBlocks uint64
	JunkBytes   int64   // Bytes before the APE descriptor (ID3v2 tags, etc.)
	SeekTable   []int64 // 64-bit frame offsets relative to descriptor start.
	FrameData   uint64  // Total compressed frame data bytes.
}

// Errors specific to APE parsing.
var (
	ErrAPENotFound   = errors.New("could not find APE descriptor (MAC magic) within 1 MB")
	ErrAPEOldVersion = errors.New("APE versions before 3.98 are not supported")
	ErrAPENoFrames   = errors.New("APE file contains no frames")
	ErrAPESeekTable  = errors.New("APE seek table is missing entries for all frames")
)

// parseAPE opens an APE file and parses its descriptor, header, and seek table.
// Only the "new" format (version >= 3.98 / 3980) is supported.
// Reverse-engineered from MAC/Source/MACLib/APEHeader.cpp.
func parseAPE(path string) (*apeInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening ape file: %w", err)
	}
	defer file.Close()

	junk, err := apeFindDescriptor(file)
	if err != nil {
		return nil, err
	}

	info := &apeInfo{JunkBytes: junk}

	// Read APE_DESCRIPTOR (52 bytes) at junk offset.
	_, err = file.Seek(junk, io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("seeking to ape descriptor: %w", err)
	}

	err = binary.Read(file, binary.LittleEndian, &info.Descriptor)
	if err != nil {
		return nil, fmt.Errorf("reading ape descriptor: %w", err)
	}

	if info.Descriptor.Version < apeMinVersion {
		return nil, fmt.Errorf("%w: version %d", ErrAPEOldVersion, info.Descriptor.Version)
	}

	// Read APE_HEADER (24 bytes) at junk + descriptor_bytes.
	_, err = file.Seek(junk+int64(info.Descriptor.DescriptorBytes), io.SeekStart)
	if err != nil {
		return nil, fmt.Errorf("seeking to ape header: %w", err)
	}

	err = binary.Read(file, binary.LittleEndian, &info.Header)
	if err != nil {
		return nil, fmt.Errorf("reading ape header: %w", err)
	}

	if info.Header.TotalFrames == 0 {
		return nil, ErrAPENoFrames
	}

	info.FrameData = uint64(info.Descriptor.FrameDataBytesHigh)<<highDWordShift | uint64(info.Descriptor.FrameDataBytes)
	info.TotalBlocks = uint64(info.Header.TotalFrames-1)*uint64(info.Header.BlocksPerFrame) +
		uint64(info.Header.FinalFrameBlocks)

	err = readAPESeekTable(file, info, junk)
	if err != nil {
		return nil, err
	}

	return info, nil
}

// readAPESeekTable reads the seek table (a uint32 LE array, one entry per frame) into
// info.SeekTable, converting to int64 with 4 GB overflow handling (APEHeader.cpp:116-131).
func readAPESeekTable(file *os.File, info *apeInfo, junk int64) error {
	// The seek table stores one uint32 entry per frame. Only read TotalFrames entries (all
	// later code needs) rather than the file-controlled SeekTableBytes count, so a crafted
	// descriptor cannot drive a giant allocation.
	maxInt := int(^uint(0) >> 1)
	if info.Header.TotalFrames > uint32(maxInt) {
		return fmt.Errorf("%w: %d frames exceeds platform limits", ErrAPESeekTable, info.Header.TotalFrames)
	}
	numEntries := int(info.Header.TotalFrames)

	declaredEntries := int64(info.Descriptor.SeekTableBytes) / bytesPerUint32
	if declaredEntries < int64(info.Header.TotalFrames) {
		return fmt.Errorf("%w: %d entries for %d frames", ErrAPESeekTable, declaredEntries, info.Header.TotalFrames)
	}

	tableOffset := junk + int64(info.Descriptor.DescriptorBytes) + int64(info.Descriptor.HeaderBytes)

	// Bound the entry count against the real file size so a corrupt TotalFrames (up to 4 billion)
	// cannot trigger a huge allocation before the read fails.
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat ape file: %w", err)
	}

	if avail := (stat.Size() - tableOffset) / bytesPerUint32; int64(numEntries) > avail {
		return fmt.Errorf("%w: %d frames exceed %d available entries", ErrAPESeekTable, numEntries, avail)
	}

	_, err = file.Seek(tableOffset, io.SeekStart)
	if err != nil {
		return fmt.Errorf("seeking to ape seek table: %w", err)
	}

	seek32 := make([]uint32, numEntries)

	err = binary.Read(file, binary.LittleEndian, seek32)
	if err != nil {
		return fmt.Errorf("reading ape seek table: %w", err)
	}

	info.SeekTable = make([]int64, numEntries)

	var (
		add  int64
		prev uint32
	)

	for i, val := range seek32 {
		if val < prev {
			add += 1 << highDWordShift // wrapped past 4 GB; add 2^32.
		}

		info.SeekTable[i] = add + int64(val)
		prev = val
	}

	return nil
}

// apeFindDescriptor locates the APE descriptor in the file, skipping any
// ID3v2 tag or other junk. Returns the byte offset of the descriptor.
// Reverse-engineered from APEHeader.cpp:17-113.
func apeFindDescriptor(file *os.File) (int64, error) {
	junk, err := apeSkipID3v2Tag(file)
	if err != nil {
		return 0, err
	}

	return apeScanForMagic(file, junk)
}

// apeSkipID3v2Tag returns the byte offset just past a leading ID3v2 tag (and any null
// padding that follows it), or 0 when the file does not start with an ID3v2 tag.
func apeSkipID3v2Tag(file *os.File) (int64, error) {
	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("seeking to file start: %w", err)
	}

	var id3hdr [id3v2HeaderLen]byte

	_, err = io.ReadFull(file, id3hdr[:])
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, nil // File is too small to hold an ID3v2 tag.
	} else if err != nil {
		return 0, fmt.Errorf("reading id3v2 header: %w", err)
	}

	if !isID3v2Header(id3hdr[:]) {
		return 0, nil
	}

	hasFooter := id3hdr[5]&id3v2FooterFlag != 0

	junk := id3v2TagSize(id3hdr[:]) + id3v2HeaderLen
	if hasFooter {
		junk += id3v2HeaderLen // The footer is the same size as the header.
	}

	_, err = file.Seek(junk, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("seeking past id3v2 tag: %w", err)
	}

	// Skip null-padding after the tag (only present when there is no footer).
	if !hasFooter {
		junk = apeSkipNullPadding(file, junk)
	}

	return junk, nil
}

// isID3v2Header reports whether hdr (a full id3v2HeaderLen-byte header) begins with an ID3v2 tag.
func isID3v2Header(hdr []byte) bool {
	return len(hdr) >= id3v2HeaderLen && hdr[0] == 'I' && hdr[1] == 'D' && hdr[2] == '3'
}

// id3v2TagSize decodes the sync-safe (7 significant bits per byte) ID3v2 tag size.
func id3v2TagSize(hdr []byte) int64 {
	return int64(hdr[6]&id3v2SizeMask)<<id3v2SizeShift1 |
		int64(hdr[7]&id3v2SizeMask)<<id3v2SizeShift2 |
		int64(hdr[8]&id3v2SizeMask)<<id3v2SizeShift3 |
		int64(hdr[9]&id3v2SizeMask)
}

// apeSkipNullPadding advances past any zero bytes following an ID3v2 tag, returning the
// offset of the first non-null byte. Read errors (e.g. EOF) end the scan at the current offset.
func apeSkipNullPadding(file *os.File, junk int64) int64 {
	var b [1]byte

	for scanned := 0; scanned < apeMaxMagicScan; scanned++ {
		n, err := file.Read(b[:])
		if err != nil || n == 0 || b[0] != 0 {
			return junk
		}

		junk++
	}

	return junk
}

// apeScanForMagic scans forward from junk for the "MAC " / "MACF" descriptor magic
// and returns its byte offset.
func apeScanForMagic(file *os.File, junk int64) (int64, error) {
	_, err := file.Seek(junk, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("seeking to descriptor scan start: %w", err)
	}

	var magic [4]byte

	_, err = io.ReadFull(file, magic[:])
	if err != nil {
		return 0, fmt.Errorf("reading ape magic: %w", err)
	}

	for scan := 0; !isAPEMagic(magic) && scan < apeMaxMagicScan; scan++ {
		var next [1]byte

		_, err = file.Read(next[:])
		if err != nil {
			return 0, ErrAPENotFound
		}

		magic[0], magic[1], magic[2], magic[3] = magic[1], magic[2], magic[3], next[0]
		junk++
	}

	if !isAPEMagic(magic) {
		return 0, ErrAPENotFound
	}

	return junk, nil
}

// isAPEMagic checks for "MAC " or "MACF".
func isAPEMagic(magic [4]byte) bool {
	return (magic == [4]byte{'M', 'A', 'C', ' '}) || (magic == [4]byte{'M', 'A', 'C', 'F'})
}

// realignFrameData reverses the APE encoder's FixupFrame byte rearrangement.
//
// The encoder's FixupFrame (APECompressCreate.cpp:173-186) byte-swaps uint32s,
// shifts bytes right by R, inserts leftover bytes from the previous frame, then
// byte-swaps back. This means frame data at non-4-byte-aligned positions is
// interleaved with previous-frame leftovers across uint32 boundaries.
//
// To produce a new file where the first frame starts at R=0 alignment, we
// reverse the process: byte-swap, shift left by R (removing the prefix), then
// byte-swap back. The input must start from seek[F]-pad (the uint32-aligned
// position) and include pad prefix bytes plus all frame data plus 4 extra
// trailing bytes to keep the tail uint32 complete.
func realignFrameData(data []byte, pad int) []byte {
	if pad == 0 {
		return data
	}

	// Stage 1: copy into a uint32-aligned buffer and byte-swap each word into logical
	// (MSB-first) byte order, matching how the decoder consumes the little-endian words.
	buf := make([]byte, roundUpToUint32(len(data)))
	copy(buf, data)
	byteSwapUint32s(buf)

	// Drop the 'pad' prefix bytes (the previous frame's tail) so our frame begins at
	// logical offset 0, then re-align to a uint32 boundary and swap back to LE words.
	dropped := buf[pad:]

	out := make([]byte, roundUpToUint32(len(dropped)))
	copy(out, dropped)
	byteSwapUint32s(out)

	return out
}

// byteSwapUint32s reverses the byte order of every 4-byte word in buf in place. Any
// trailing bytes that don't fill a final word are left untouched.
func byteSwapUint32s(buf []byte) {
	for i := 0; i+3 < len(buf); i += bytesPerUint32 {
		buf[i], buf[i+3] = buf[i+3], buf[i]
		buf[i+1], buf[i+2] = buf[i+2], buf[i+1]
	}
}

// roundUpToUint32 rounds n up to the next multiple of 4 (a uint32 boundary).
func roundUpToUint32(n int) int {
	if rem := n % bytesPerUint32; rem != 0 {
		return n + (bytesPerUint32 - rem)
	}

	return n
}

// copyFrameData copies compressed frame data from srcFile to dst for R=0 tracks. io.CopyN
// copies exactly the requested size and returns a nil error even when the source's final
// Read reports data together with io.EOF, so a frame that ends at EOF is not a failure.
func copyFrameData(dst io.Writer, srcFile *os.File, info *apeInfo, startFrame, endFrame int) error {
	for frameIdx := startFrame; frameIdx <= endFrame; frameIdx++ {
		srcOffset := info.SeekTable[frameIdx] + info.JunkBytes
		size := apeFrameDataSize(info, frameIdx)

		_, err := srcFile.Seek(srcOffset, io.SeekStart)
		if err != nil {
			return fmt.Errorf("seeking to frame %d in source: %w", frameIdx, err)
		}

		_, err = io.CopyN(dst, srcFile, size)
		if err != nil {
			return fmt.Errorf("copying frame %d from source: %w", frameIdx, err)
		}
	}

	return nil
}

// apeFrameDataSize returns the compressed data size of a single frame.
func apeFrameDataSize(info *apeInfo, frameIdx int) int64 {
	if frameIdx < int(info.Header.TotalFrames)-1 {
		return info.SeekTable[frameIdx+1] - info.SeekTable[frameIdx]
	}
	// Last frame: from its offset to end of frame data region.
	return int64(info.FrameData) - (info.SeekTable[frameIdx] - info.SeekTable[0])
}

// apeFrameRange is the inclusive range of APE frames that make up one CUE track.
// APE can only be split on whole-frame boundaries (no decode), so track boundaries are
// snapped to the frame containing the CUE timestamp.
type apeFrameRange struct {
	start int // inclusive
	end   int // inclusive
}

// apeTrackFrameRanges maps each CUE track to the inclusive APE frame range it occupies.
func apeTrackFrameRanges(cue *CueSheet, timestamps []cueTimestamp, info *apeInfo) []apeFrameRange {
	bpf := uint64(info.Header.BlocksPerFrame)
	lastFrame := int(info.Header.TotalFrames) - 1

	// Convert CUE timestamps (sample positions) to frame indices.
	trackStarts := make([]uint64, len(cue.Tracks))
	for i, ts := range timestamps {
		trackStarts[i] = ts.toSamples(info.Header.SampleRate)
	}

	ranges := make([]apeFrameRange, len(cue.Tracks))

	for idx := range cue.Tracks {
		startFrame := int(trackStarts[idx] / bpf)
		if idx == 0 {
			startFrame = 0 // The first track always includes any lead-in/pregap frames.
		}

		endFrame := lastFrame
		if idx+1 < len(cue.Tracks) {
			endFrame = int(trackStarts[idx+1]/bpf) - 1
		}

		if startFrame > lastFrame {
			startFrame = lastFrame
		}

		if endFrame < startFrame {
			endFrame = startFrame
		}

		ranges[idx] = apeFrameRange{start: startFrame, end: endFrame}
	}

	return ranges
}

// splitAPE splits an APE file into individual tracks based on CUE sheet data.
// Compressed frames are copied verbatim — no audio decoding or re-encoding.
func splitAPE(
	xFile *XFile,
	audioPath string,
	cue *CueSheet,
	timestamps []cueTimestamp,
) (uint64, []string, error) {
	info, err := parseAPE(audioPath)
	if err != nil {
		return 0, nil, err
	}

	ranges := apeTrackFrameRanges(cue, timestamps, info)

	err = os.MkdirAll(xFile.OutputDir, xFile.DirMode)
	if err != nil {
		return 0, nil, fmt.Errorf("creating output directory: %w", err)
	}

	defer xFile.newProgress(0, 0, len(cue.Tracks)).done()

	srcFile, err := os.Open(audioPath)
	if err != nil {
		return 0, nil, fmt.Errorf("opening ape file for splitting: %w", err)
	}
	defer srcFile.Close()

	var (
		totalSize uint64
		files     = make([]string, 0, len(cue.Tracks))
	)

	for i := range cue.Tracks {
		track := &cue.Tracks[i]
		fr := ranges[i]

		outputName := formatTrackFilename(track, ".ape")
		outputPath := filepath.Join(xFile.OutputDir, outputName)

		size, writeErr := writeTrackAPE(outputPath, info, srcFile, fr.start, fr.end, xFile.FileMode)
		if writeErr != nil {
			return totalSize, files, fmt.Errorf("writing ape track %d: %w", track.Number, writeErr)
		}

		totalSize += size

		files = append(files, outputPath)
		xFile.Debugf("Wrote APE track %d: %s (%d bytes)", track.Number, outputPath, size)
	}

	return totalSize, files, nil
}

// writeTrackAPE writes a new APE file containing frames startFrame..endFrame (inclusive)
// from the source file. Compressed frame data is copied verbatim. On error the partial
// output file is removed so a failed split never leaves a half-written track behind.
func writeTrackAPE(
	outputPath string,
	info *apeInfo,
	srcFile *os.File,
	startFrame, endFrame int,
	fileMode os.FileMode,
) (uint64, error) {
	outFile, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return 0, fmt.Errorf("creating output ape file: %w", err)
	}

	size, err := writeTrackAPEContents(outFile, info, srcFile, startFrame, endFrame)

	closeErr := outFile.Close()
	if err == nil && closeErr != nil {
		err = fmt.Errorf("closing output ape file: %w", closeErr)
	}

	if err != nil {
		_ = os.Remove(outputPath)
		return 0, err
	}

	return size, nil
}

// apeTrackContainer holds the serialized, ready-to-write container pieces for one split
// track: the descriptor, plus the header and seek-table bytes (kept so they can be both
// written to disk and fed to the MD5, which the format hashes after the frame data).
type apeTrackContainer struct {
	descriptor     apeDescriptor
	headerBytes    []byte
	seekTableBytes []byte
	trackDataSize  int64
}

// buildAPETrackContainer computes the descriptor, header and seek table for a new APE file
// containing source frames startFrame..endFrame (inclusive).
func buildAPETrackContainer(info *apeInfo, startFrame, endFrame int) (*apeTrackContainer, error) {
	numFrames := endFrame - startFrame + 1

	// The final frame of the source keeps its (short) block count; interior frames are full.
	ffb := info.Header.BlocksPerFrame
	if endFrame == int(info.Header.TotalFrames)-1 {
		ffb = info.Header.FinalFrameBlocks
	}

	// New file layout: [DESCRIPTOR 52] [HEADER 24] [SEEK TABLE 4*N] [FRAME DATA].
	seekTableSize := uint32(numFrames) * bytesPerUint32
	dataOffset := int64(apeDescriptorSize) + int64(apeHeaderSize) + int64(seekTableSize)

	// Seek table: absolute offsets from file start (= descriptor start, no junk).
	seekTable := make([]uint32, numFrames)

	var trackDataSize int64

	for i := range numFrames {
		seekTable[i] = uint32((dataOffset + trackDataSize) & maxUint32)
		trackDataSize += apeFrameDataSize(info, startFrame+i)
	}

	hdr := apeHeader{
		CompressionLevel: info.Header.CompressionLevel,
		FormatFlags:      info.Header.FormatFlags | apeFormatFlagCreateWAV,
		BlocksPerFrame:   info.Header.BlocksPerFrame,
		FinalFrameBlocks: ffb,
		TotalFrames:      uint32(numFrames),
		BitsPerSample:    info.Header.BitsPerSample,
		Channels:         info.Header.Channels,
		SampleRate:       info.Header.SampleRate,
	}

	headerBytes, seekTableBytes, err := marshalAPEHeaderAndSeekTable(&hdr, seekTable)
	if err != nil {
		return nil, err
	}

	return &apeTrackContainer{
		descriptor: apeDescriptor{
			ID:                 info.Descriptor.ID,
			Version:            info.Descriptor.Version,
			DescriptorBytes:    apeDescriptorSize,
			HeaderBytes:        apeHeaderSize,
			SeekTableBytes:     seekTableSize,
			HeaderDataBytes:    0, // CREATE_WAV_HEADER flag is set in the header above.
			FrameDataBytes:     uint32(trackDataSize & maxUint32),
			FrameDataBytesHigh: uint32((trackDataSize >> highDWordShift) & maxUint32),
			TerminatingBytes:   0,
			// FileMD5 stays zero here; it's computed and patched in after the data is written.
		},
		headerBytes:    headerBytes,
		seekTableBytes: seekTableBytes,
		trackDataSize:  trackDataSize,
	}, nil
}

// writeTrackAPEContents writes the descriptor, header, seek table and frame data for a
// single split track into outFile and returns the total bytes written. The whole-file MD5
// is computed and patched into the descriptor so the output passes full MAC verification.
func writeTrackAPEContents(
	outFile *os.File,
	info *apeInfo,
	srcFile *os.File,
	startFrame, endFrame int,
) (uint64, error) {
	con, err := buildAPETrackContainer(info, startFrame, endFrame)
	if err != nil {
		return 0, err
	}

	err = binary.Write(outFile, binary.LittleEndian, &con.descriptor)
	if err != nil {
		return 0, fmt.Errorf("writing ape descriptor: %w", err)
	}

	_, err = outFile.Write(con.headerBytes)
	if err != nil {
		return 0, fmt.Errorf("writing ape header: %w", err)
	}

	_, err = outFile.Write(con.seekTableBytes)
	if err != nil {
		return 0, fmt.Errorf("writing ape seek table: %w", err)
	}

	// Tee the frame data into the MD5 as it's written so we never buffer a whole track.
	hash := md5.New() //nolint:gosec // MD5 is the APE file integrity hash, not security.
	dst := io.MultiWriter(outFile, hash)

	err = writeAPEFrameData(dst, srcFile, info, startFrame, endFrame, con.trackDataSize)
	if err != nil {
		return 0, err
	}

	// The format hashes (header data) + frame data + (terminating data) + header + seek table.
	// This file has no header/terminating data, so: frame data + header + seek table.
	_, _ = hash.Write(con.headerBytes)
	_, _ = hash.Write(con.seekTableBytes)

	err = patchAPEFileMD5(outFile, hash.Sum(nil))
	if err != nil {
		return 0, err
	}

	stat, err := outFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat output ape file: %w", err)
	}

	return uint64(stat.Size()), nil
}

// marshalAPEHeaderAndSeekTable serializes the APE header and seek table to little-endian bytes.
func marshalAPEHeaderAndSeekTable(hdr *apeHeader, seekTable []uint32) ([]byte, []byte, error) {
	var headerBuf bytes.Buffer

	err := binary.Write(&headerBuf, binary.LittleEndian, hdr)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding ape header: %w", err)
	}

	var seekBuf bytes.Buffer

	err = binary.Write(&seekBuf, binary.LittleEndian, seekTable)
	if err != nil {
		return nil, nil, fmt.Errorf("encoding ape seek table: %w", err)
	}

	return headerBuf.Bytes(), seekBuf.Bytes(), nil
}

// writeAPEFrameData writes exactly trackDataSize bytes of compressed frame data to dst,
// reversing the FixupFrame byte rearrangement when the first frame is not 4-byte aligned.
func writeAPEFrameData(
	dst io.Writer,
	srcFile *os.File,
	info *apeInfo,
	startFrame, endFrame int,
	trackDataSize int64,
) error {
	// The APE encoder's FixupFrame rearranges bytes across uint32 boundaries when frames
	// don't start on a 4-byte alignment. Calculate the alignment offset (pad) for the first
	// frame; the decoder applies the same remainder in SeekToFrame (APEDecompress.cpp).
	pad := int((info.SeekTable[startFrame] - info.SeekTable[0]) % bytesPerUint32)
	if pad == 0 {
		// R=0: the first frame is already word-aligned, so a byte-for-byte copy is valid.
		return copyFrameData(dst, srcFile, info, startFrame, endFrame)
	}

	// R≠0: stream from the uint32-aligned position (pad prefix bytes belong to the previous
	// frame) and reverse the byte rearrangement on the fly. Streaming keeps memory bounded
	// instead of buffering the whole (potentially huge) track at once.
	alignedStart := info.SeekTable[startFrame] + info.JunkBytes - int64(pad)

	_, err := srcFile.Seek(alignedStart, io.SeekStart)
	if err != nil {
		return fmt.Errorf("seeking to aligned frame data: %w", err)
	}

	return streamRealignFrameData(dst, srcFile, pad, trackDataSize)
}

// readRealignChunk reads the next source block into inBuf, zero-pads a short final read up to
// a whole uint32, and byte-swaps it into logical (MSB-first) order. It reports atEnd on an EOF
// (full or partial) and returns any other I/O error so it is never mistaken for end-of-stream.
func readRealignChunk(src io.Reader, inBuf []byte) (chunk []byte, atEnd bool, err error) {
	readN, readErr := io.ReadFull(src, inBuf)

	atEnd = errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)
	if readErr != nil && !atEnd {
		return nil, false, fmt.Errorf("reading frame data for realignment: %w", readErr)
	}

	swapLen := roundUpToUint32(readN)
	for i := readN; i < swapLen; i++ {
		inBuf[i] = 0
	}

	chunk = inBuf[:swapLen]
	byteSwapUint32s(chunk)

	return chunk, atEnd, nil
}

// streamRealignFrameData is the streaming equivalent of realignFrameData: it reads the
// uint32-aligned source bytes for a misaligned track, reverses the FixupFrame byte
// rearrangement in fixed-size chunks, and writes exactly dataSize bytes to dst. It never
// buffers the whole track. realignFrameData is kept as the in-memory reference that this
// function is property-tested against.
func streamRealignFrameData(dst io.Writer, src io.Reader, pad int, dataSize int64) error {
	inBuf := make([]byte, apeCopyBuf) // length is a multiple of 4

	var (
		carry   = make([]byte, 0, bytesPerUint32) // logical bytes (< 4) carried between reads
		dropped int                               // count of the pad prefix bytes dropped so far
		written int64
	)

	for written < dataSize {
		chunk, atEnd, err := readRealignChunk(src, inBuf)
		if err != nil {
			return err
		}

		// Drop the pad-byte prefix (previous frame's tail) exactly once, at the very start.
		offset := 0
		if dropped < pad {
			offset = min(pad-dropped, len(chunk))
			dropped += offset
		}

		// Combine carried logical bytes with this chunk, emit whole words (all of it at EOF).
		work := make([]byte, 0, len(carry)+len(chunk))
		work = append(work, carry...)
		work = append(work, chunk[offset:]...)

		flush := len(work) - len(work)%bytesPerUint32
		if atEnd {
			flush = roundUpToUint32(len(work))
		}

		out := make([]byte, flush)
		copy(out, work)
		byteSwapUint32s(out) // back to little-endian file order

		toWrite := min(int64(len(out)), dataSize-written)

		_, writeErr := dst.Write(out[:toWrite])
		if writeErr != nil {
			return fmt.Errorf("writing realigned frame data: %w", writeErr)
		}

		written += toWrite

		if atEnd {
			break
		}

		// Carry the leftover (< 4) logical bytes that didn't fill a whole word into the next read.
		carry = append(carry[:0], work[flush:]...)
	}

	if written < dataSize {
		return fmt.Errorf("reading frame data for realignment: %w", io.ErrUnexpectedEOF)
	}

	return nil
}

// patchAPEFileMD5 writes the 16-byte MD5 into the descriptor's cFileMD5 field (offset 36).
func patchAPEFileMD5(outFile *os.File, sum []byte) error {
	_, err := outFile.Seek(apeDescriptorSize-apeFileMD5Size, io.SeekStart)
	if err != nil {
		return fmt.Errorf("seeking to ape md5 field: %w", err)
	}

	_, err = outFile.Write(sum[:apeFileMD5Size])
	if err != nil {
		return fmt.Errorf("writing ape md5: %w", err)
	}

	return nil
}
