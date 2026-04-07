package xtractr

// APE (Monkey's Audio) binary-level splitting.
//
// Parses the APE container format (reverse-engineered from the Monkey's Audio
// source code: MAC/Source/MACLib/MACLib.h and APEHeader.cpp) and splits a
// single APE image file into individual per-track APE files by copying
// compressed frames verbatim — no audio decoding or re-encoding needed.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// APE format constants from MAC/Source/MACLib/MACLib.h.
const (
	apeMinVersion            = 3980 // Only the "new" format (>= 3.98) is supported.
	apeDescriptorSize        = 52   // sizeof(APE_DESCRIPTOR)
	apeHeaderSize            = 24   // sizeof(APE_HEADER)
	apeFormatFlagCreateWAV   = 1 << 5
	apeMaxMagicScan          = 1 << 20 // Scan up to 1 MB for the magic bytes.
	apeCopyBuf               = 1 << 20 // 1 MB copy buffer.
)

// apeDescriptor mirrors APE_DESCRIPTOR (MACLib.h:179-194), 52 bytes, little-endian.
type apeDescriptor struct {
	ID                  [4]byte
	Version             uint16
	Padding             uint16
	DescriptorBytes     uint32
	HeaderBytes         uint32
	SeekTableBytes      uint32
	HeaderDataBytes     uint32
	FrameDataBytes      uint32
	FrameDataBytesHigh  uint32
	TerminatingBytes    uint32
	FileMD5             [16]byte
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
	Descriptor apeDescriptor
	Header     apeHeader
	TotalBlocks uint64
	JunkBytes   int64         // Bytes before the APE descriptor (ID3v2 tags, etc.)
	SeekTable   []int64       // 64-bit frame offsets relative to descriptor start.
	FrameData   uint64        // Total compressed frame data bytes.
}

// Errors specific to APE parsing.
var (
	ErrAPENotFound      = errors.New("could not find APE descriptor (MAC magic) within 1 MB")
	ErrAPEOldVersion    = errors.New("APE versions before 3.98 are not supported")
	ErrAPENoFrames      = errors.New("APE file contains no frames")
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
	if _, err = file.Seek(junk, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking to ape descriptor: %w", err)
	}

	if err = binary.Read(file, binary.LittleEndian, &info.Descriptor); err != nil {
		return nil, fmt.Errorf("reading ape descriptor: %w", err)
	}

	if info.Descriptor.Version < apeMinVersion {
		return nil, fmt.Errorf("%w: version %d", ErrAPEOldVersion, info.Descriptor.Version)
	}

	// Read APE_HEADER (24 bytes) at junk + descriptor_bytes.
	if _, err = file.Seek(junk+int64(info.Descriptor.DescriptorBytes), io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking to ape header: %w", err)
	}

	if err = binary.Read(file, binary.LittleEndian, &info.Header); err != nil {
		return nil, fmt.Errorf("reading ape header: %w", err)
	}

	if info.Header.TotalFrames == 0 {
		return nil, ErrAPENoFrames
	}

	info.FrameData = uint64(info.Descriptor.FrameDataBytesHigh)<<32 | uint64(info.Descriptor.FrameDataBytes)
	info.TotalBlocks = uint64(info.Header.TotalFrames-1)*uint64(info.Header.BlocksPerFrame) + uint64(info.Header.FinalFrameBlocks)

	// Read seek table: array of uint32 LE values (APEHeader.cpp:278-285).
	numEntries := int(info.Descriptor.SeekTableBytes / 4)
	if _, err = file.Seek(junk+int64(info.Descriptor.DescriptorBytes)+int64(info.Descriptor.HeaderBytes), io.SeekStart); err != nil {
		return nil, fmt.Errorf("seeking to ape seek table: %w", err)
	}

	seek32 := make([]uint32, numEntries)
	if err = binary.Read(file, binary.LittleEndian, seek32); err != nil {
		return nil, fmt.Errorf("reading ape seek table: %w", err)
	}

	// Convert uint32 → int64 with 4 GB overflow handling (APEHeader.cpp:116-131).
	info.SeekTable = make([]int64, numEntries)

	var add int64
	var prev uint32

	for i, val := range seek32 {
		if val < prev {
			add += 0x100000000
		}

		info.SeekTable[i] = add + int64(val)
		prev = val
	}

	return info, nil
}

// apeFindDescriptor locates the APE descriptor in the file, skipping any
// ID3v2 tag or other junk. Returns the byte offset of the descriptor.
// Reverse-engineered from APEHeader.cpp:17-113.
func apeFindDescriptor(file *os.File) (int64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}

	var junk int64

	// Check for ID3v2 tag.
	var id3hdr [10]byte

	n, _ := file.Read(id3hdr[:])
	if n >= 10 && id3hdr[0] == 'I' && id3hdr[1] == 'D' && id3hdr[2] == '3' {
		length := int64(id3hdr[6]&0x7F)<<21 |
			int64(id3hdr[7]&0x7F)<<14 |
			int64(id3hdr[8]&0x7F)<<7 |
			int64(id3hdr[9]&0x7F)

		hasFooter := id3hdr[5]&0x10 != 0
		if hasFooter {
			junk = length + 20
		} else {
			junk = length + 10
		}

		if _, err := file.Seek(junk, io.SeekStart); err != nil {
			return 0, err
		}

		// Skip null-padding after tag (when no footer).
		if !hasFooter {
			var b [1]byte
			for {
				if _, err := file.Read(b[:]); err != nil || b[0] != 0 {
					break
				}

				junk++
			}
		}
	}

	// Scan for "MAC " or "MACF" magic.
	if _, err := file.Seek(junk, io.SeekStart); err != nil {
		return 0, err
	}

	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return 0, fmt.Errorf("reading ape magic: %w", err)
	}

	for scan := 0; !isAPEMagic(magic) && scan < apeMaxMagicScan; scan++ {
		var b [1]byte
		if _, err := file.Read(b[:]); err != nil {
			return 0, ErrAPENotFound
		}

		magic[0], magic[1], magic[2], magic[3] = magic[1], magic[2], magic[3], b[0]
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

	// Work on a copy padded to uint32 boundary.
	buf := make([]byte, len(data))
	copy(buf, data)

	for len(buf)%4 != 0 { //nolint:mnd
		buf = append(buf, 0)
	}

	// Byte-swap each uint32 (LE → BE).
	for i := 0; i+3 < len(buf); i += 4 { //nolint:mnd
		buf[i], buf[i+3] = buf[i+3], buf[i]
		buf[i+1], buf[i+2] = buf[i+2], buf[i+1]
	}

	// Remove the first 'pad' bytes.
	buf = buf[pad:]

	// Re-pad to uint32 boundary.
	for len(buf)%4 != 0 { //nolint:mnd
		buf = append(buf, 0)
	}

	// Byte-swap back (BE → LE).
	for i := 0; i+3 < len(buf); i += 4 { //nolint:mnd
		buf[i], buf[i+3] = buf[i+3], buf[i]
		buf[i+1], buf[i+2] = buf[i+2], buf[i+1]
	}

	return buf
}

// copyFrameData copies compressed frame data from srcFile to outFile for R=0 tracks.
func copyFrameData(outFile, srcFile *os.File, info *apeInfo, startFrame, endFrame int) error {
	buf := make([]byte, apeCopyBuf)

	for fi := startFrame; fi <= endFrame; fi++ {
		srcOffset := info.SeekTable[fi] + info.JunkBytes
		size := apeFrameDataSize(info, fi)

		if _, err := srcFile.Seek(srcOffset, io.SeekStart); err != nil {
			return fmt.Errorf("seeking to frame %d in source: %w", fi, err)
		}

		remaining := size
		for remaining > 0 {
			toRead := int64(len(buf))
			if toRead > remaining {
				toRead = remaining
			}

			n, readErr := srcFile.Read(buf[:toRead])
			if n > 0 {
				if _, writeErr := outFile.Write(buf[:n]); writeErr != nil {
					return fmt.Errorf("writing frame data: %w", writeErr)
				}

				remaining -= int64(n)
			}

			if readErr != nil {
				return fmt.Errorf("reading frame %d from source: %w", fi, readErr)
			}
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

	sampleRate := info.Header.SampleRate
	bpf := info.Header.BlocksPerFrame

	// Convert CUE timestamps to block offsets and then to frame indices.
	trackStarts := make([]uint64, len(cue.Tracks))
	for i, ts := range timestamps {
		trackStarts[i] = ts.toSamples(sampleRate)
	}

	type frameRange struct {
		start int // inclusive
		end   int // inclusive
	}

	ranges := make([]frameRange, len(cue.Tracks))

	for i := range cue.Tracks {
		sf := int(trackStarts[i] / uint64(bpf))
		if i == 0 {
			sf = 0
		}

		var ef int
		if i+1 < len(cue.Tracks) {
			nextSF := int(trackStarts[i+1] / uint64(bpf))
			ef = nextSF - 1
		} else {
			ef = int(info.Header.TotalFrames) - 1
		}

		if sf > int(info.Header.TotalFrames)-1 {
			sf = int(info.Header.TotalFrames) - 1
		}

		if ef < sf {
			ef = sf
		}

		ranges[i] = frameRange{start: sf, end: ef}
	}

	if err = os.MkdirAll(xFile.OutputDir, xFile.DirMode); err != nil {
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
// from the source file. Compressed frame data is copied verbatim.
func writeTrackAPE(
	outputPath string,
	info *apeInfo,
	srcFile *os.File,
	startFrame, endFrame int,
	fileMode os.FileMode,
) (uint64, error) {
	numFrames := endFrame - startFrame + 1

	// Determine final-frame blocks for the new file.
	var ffb uint32
	if endFrame == int(info.Header.TotalFrames)-1 {
		ffb = info.Header.FinalFrameBlocks
	} else {
		ffb = info.Header.BlocksPerFrame
	}

	// Calculate total compressed data size for these frames.
	var trackDataSize int64
	for fi := startFrame; fi <= endFrame; fi++ {
		trackDataSize += apeFrameDataSize(info, fi)
	}

	// New file layout:
	//   [DESCRIPTOR 52] [HEADER 24] [SEEK TABLE 4*N] [FRAME DATA]
	seekTableBytes := uint32(numFrames * 4) //nolint:mnd
	dataOffset := int64(apeDescriptorSize + apeHeaderSize + int(seekTableBytes))

	// Build new seek table: absolute offsets from file start (= descriptor start, no junk).
	seekTable := make([]uint32, numFrames)

	var cum int64
	for i := 0; i < numFrames; i++ {
		seekTable[i] = uint32((dataOffset + cum) & 0xFFFFFFFF) //nolint:mnd
		cum += apeFrameDataSize(info, startFrame+i)
	}

	// Write the output file.
	outFile, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return 0, fmt.Errorf("creating output ape file: %w", err)
	}

	// Descriptor.
	desc := apeDescriptor{
		ID:                 info.Descriptor.ID,
		Version:            info.Descriptor.Version,
		DescriptorBytes:    apeDescriptorSize,
		HeaderBytes:        apeHeaderSize,
		SeekTableBytes:     seekTableBytes,
		HeaderDataBytes:    0, // CREATE_WAV_HEADER flag is set below.
		FrameDataBytes:     uint32(trackDataSize & 0xFFFFFFFF),          //nolint:mnd
		FrameDataBytesHigh: uint32((trackDataSize >> 32) & 0xFFFFFFFF),  //nolint:mnd
		TerminatingBytes:   0,
	}

	if err = binary.Write(outFile, binary.LittleEndian, &desc); err != nil {
		_ = outFile.Close()
		return 0, fmt.Errorf("writing ape descriptor: %w", err)
	}

	// Header.
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

	if err = binary.Write(outFile, binary.LittleEndian, &hdr); err != nil {
		_ = outFile.Close()
		return 0, fmt.Errorf("writing ape header: %w", err)
	}

	// Seek table.
	if err = binary.Write(outFile, binary.LittleEndian, seekTable); err != nil {
		_ = outFile.Close()
		return 0, fmt.Errorf("writing ape seek table: %w", err)
	}

	// The APE encoder's FixupFrame rearranges bytes across uint32 boundaries
	// when frames don't start on a 4-byte alignment. Calculate the alignment
	// offset (pad) for the first frame.  See APEDecompress.cpp:269.
	pad := int((info.SeekTable[startFrame] - info.SeekTable[0]) % 4) //nolint:mnd

	if pad == 0 {
		// R=0: simple byte-for-byte copy.
		if err = copyFrameData(outFile, srcFile, info, startFrame, endFrame); err != nil {
			_ = outFile.Close()
			return 0, err
		}
	} else {
		// R≠0: read the track's data from the aligned position (pad prefix bytes
		// + frame data + 4 trailing bytes), reverse the FixupFrame byte
		// rearrangement, then write.
		alignedStart := info.SeekTable[startFrame] + info.JunkBytes - int64(pad)
		totalRead := int64(pad) + trackDataSize + 4 //nolint:mnd

		if _, err = srcFile.Seek(alignedStart, io.SeekStart); err != nil {
			_ = outFile.Close()
			return 0, fmt.Errorf("seeking to aligned frame data: %w", err)
		}

		raw := make([]byte, totalRead)

		n, readErr := io.ReadFull(srcFile, raw)
		if readErr != nil && n < int(int64(pad)+trackDataSize) {
			_ = outFile.Close()
			return 0, fmt.Errorf("reading frame data for realignment: %w", readErr)
		}

		raw = raw[:n]
		realigned := realignFrameData(raw, pad)

		if _, err = outFile.Write(realigned[:trackDataSize]); err != nil {
			_ = outFile.Close()
			return 0, fmt.Errorf("writing realigned frame data: %w", err)
		}
	}

	if err = outFile.Close(); err != nil {
		return 0, fmt.Errorf("closing output ape file: %w", err)
	}

	stat, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat output ape file: %w", err)
	}

	return uint64(stat.Size()), nil
}
