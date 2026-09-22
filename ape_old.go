package xtractr

// Legacy APE container (Monkey's Audio 3.81 through 3.97).
//
// The codec bitstream is unchanged from the splitter's point of view: frames are still
// copied verbatim and the file version is preserved so a decoder selects the 3.97 codec.
// Only the header differs. Layout, from MAC APEHeader.cpp AnalyzeOld and ffmpeg's
// libavformat/ape.c:
//
//	[32-byte header]
//	[optional uint32 peak level]
//	[optional uint32 seek-element count]
//	[stored WAV header, unless CREATE_WAV_HEADER is set]
//	[uint32 seek table, one entry per frame, absolute offsets from the MAC header]
//	[compressed frames]
//	[terminating bytes]
//	[optional APEv2 / ID3v1 tags]
//
// Versions before 3.81 add a per-frame bit table and are rejected.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	apeTagFooterLen     = 32
	apeTagVersion1      = 1000
	apeTagVersion2      = 2000
	id3v1TagLen         = 128
	apeTagFlagHasHeader = 1 << 31
	apeTagFlagIsHeader  = 1 << 29
	apeBits8            = 8
	apeBits16           = 16
	apeBits24           = 24
)

// errAPETruncated is returned when a legacy header describes audio that does not fit
// in the file.
var errAPETruncated = errors.New("APE frame data is truncated")

// apeHeaderOld mirrors APE_HEADER_OLD, the 32-byte pre-3.98 header, little-endian.
type apeHeaderOld struct {
	ID               [4]byte
	Version          uint16
	CompressionLevel uint16
	FormatFlags      uint16
	Channels         uint16
	SampleRate       uint32
	HeaderBytes      uint32
	TerminatingBytes uint32
	TotalFrames      uint32
	FinalFrameBlocks uint32
}

// parseAPEOld reads a legacy APE header, seek table, and frame-data length into info.
func parseAPEOld(file *os.File, junk int64, version uint16) (*apeInfo, error) {
	if version < apeOldMinVersion {
		return nil, fmt.Errorf("%w: version %d", ErrAPEOldVersion, version)
	}

	hdr, err := readAPEOldHeader(file, junk)
	if err != nil {
		return nil, err
	}

	if hdr.TotalFrames == 0 {
		return nil, ErrAPENoFrames
	}

	info, err := newOldAPEInfo(file, junk, &hdr)
	if err != nil {
		return nil, err
	}

	err = readAPESeekTable(file, info, junk)
	if err != nil {
		return nil, err
	}

	err = setOldAPEFrameData(file, info)
	if err != nil {
		return nil, err
	}

	return info, nil
}

// readAPEOldHeader reads the 32-byte legacy header at the MAC magic.
func readAPEOldHeader(file *os.File, junk int64) (apeHeaderOld, error) {
	_, err := file.Seek(junk, io.SeekStart)
	if err != nil {
		return apeHeaderOld{}, fmt.Errorf("seeking to ape header: %w", err)
	}

	var hdr apeHeaderOld

	err = binary.Read(file, binary.LittleEndian, &hdr)
	if err != nil {
		return apeHeaderOld{}, fmt.Errorf("reading ape header: %w", err)
	}

	return hdr, nil
}

// newOldAPEInfo fills descriptor and header fields from a legacy header. DescriptorBytes
// stays 0 and HeaderBytes is the distance from the MAC magic to the seek table, which is
// the offset readAPESeekTable already uses.
func newOldAPEInfo(file *os.File, junk int64, hdr *apeHeaderOld) (*apeInfo, error) {
	headerBytes, seekBytes, err := apeOldSeekTableLoc(file, junk, hdr)
	if err != nil {
		return nil, err
	}

	blocks := apeOldBlocksPerFrame(hdr.Version, hdr.CompressionLevel)

	return &apeInfo{
		JunkBytes: junk,
		Descriptor: apeDescriptor{
			ID:               hdr.ID,
			Version:          hdr.Version,
			HeaderBytes:      headerBytes,
			SeekTableBytes:   seekBytes,
			TerminatingBytes: hdr.TerminatingBytes,
		},
		Header: apeHeader{
			CompressionLevel: hdr.CompressionLevel,
			FormatFlags:      hdr.FormatFlags,
			BlocksPerFrame:   blocks,
			FinalFrameBlocks: hdr.FinalFrameBlocks,
			TotalFrames:      hdr.TotalFrames,
			BitsPerSample:    apeOldBitsPerSample(hdr.FormatFlags),
			Channels:         hdr.Channels,
			SampleRate:       hdr.SampleRate,
		},
		TotalBlocks: uint64(hdr.TotalFrames-1)*uint64(blocks) + uint64(hdr.FinalFrameBlocks),
	}, nil
}

// apeOldSeekTableLoc returns the byte offset of the seek table from the MAC magic and
// the seek table's length. Optional peak, seek-count, and WAV bytes sit between the
// 32-byte header and the table.
func apeOldSeekTableLoc(file *os.File, junk int64, hdr *apeHeaderOld) (uint32, uint32, error) {
	extra, seekCount, err := readAPEOldPrefix(file, junk, hdr)
	if err != nil {
		return 0, 0, err
	}

	wavBytes := int64(0)
	if hdr.FormatFlags&apeFormatFlagCreateWAV == 0 {
		wavBytes = int64(hdr.HeaderBytes)
	}

	total := int64(apeOldHeaderSize) + extra + wavBytes
	if total <= 0 || total > int64(maxUint32) {
		return 0, 0, fmt.Errorf("%w: header is %d bytes", errAPETruncated, total)
	}

	if seekCount > maxUint32/bytesPerUint32 {
		return 0, 0, fmt.Errorf("%w: %d entries", ErrAPESeekTable, seekCount)
	}

	stat, err := file.Stat()
	if err != nil {
		return 0, 0, fmt.Errorf("stat ape file: %w", err)
	}

	remain := stat.Size() - junk - total
	if remain < 0 || int64(seekCount) > remain/bytesPerUint32 {
		return 0, 0, fmt.Errorf("%w: %d entries exceed file", ErrAPESeekTable, seekCount)
	}

	return uint32(total), seekCount * bytesPerUint32, nil
}

// readAPEOldPrefix walks the optional fields that follow the 32-byte header and returns
// how many bytes they occupy plus the number of seek-table entries.
func readAPEOldPrefix(file *os.File, junk int64, hdr *apeHeaderOld) (int64, uint32, error) {
	extra := int64(0)
	if hdr.FormatFlags&apeFormatFlagHasPeakLevel != 0 {
		extra += bytesPerUint32
	}

	if hdr.FormatFlags&apeFormatFlagHasSeekElements == 0 {
		return extra, hdr.TotalFrames, nil
	}

	count, err := readAPEOldSeekCount(file, junk+int64(apeOldHeaderSize)+extra)
	if err != nil {
		return 0, 0, err
	}

	if count < hdr.TotalFrames {
		return 0, 0, fmt.Errorf("%w: %d entries for %d frames", ErrAPESeekTable, count, hdr.TotalFrames)
	}

	return extra + bytesPerUint32, count, nil
}

// readAPEOldSeekCount reads the uint32 seek-element count at offset.
func readAPEOldSeekCount(file *os.File, offset int64) (uint32, error) {
	_, err := file.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, fmt.Errorf("seeking to ape seek count: %w", err)
	}

	var count uint32

	err = binary.Read(file, binary.LittleEndian, &count)
	if err != nil {
		return 0, fmt.Errorf("reading ape seek count: %w", err)
	}

	return count, nil
}

// setOldAPEFrameData sets FrameData to the compressed bytes from frame 0 through the
// end of the audio, excluding terminating data and any trailing tag.
func setOldAPEFrameData(file *os.File, info *apeInfo) error {
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat ape file: %w", err)
	}

	trailing, err := apeTrailingBytes(file, stat.Size())
	if err != nil {
		return err
	}

	audioEnd := stat.Size() - trailing - int64(info.Descriptor.TerminatingBytes)
	frame0 := info.JunkBytes + info.SeekTable[0]

	if info.SeekTable[0] < 0 || audioEnd <= frame0 {
		return fmt.Errorf("%w: audio ends at %d, frame 0 at %d", errAPETruncated, audioEnd, frame0)
	}

	info.FrameData = uint64(audioEnd - frame0)

	return nil
}

// apeOldBlocksPerFrame is the frame size MAC derives when the header does not store it.
// AnalyzeOld: 294912 samples at >= 3.95, 73728 at >= 3.90 (and at extra-high in the 3.8x
// line), otherwise 9216.
func apeOldBlocksPerFrame(version, compression uint16) uint32 {
	if version >= apeVersion3950 {
		return apeBlocksV3950
	}

	if version >= apeVersion3900 || compression == apeCompressionExtraHigh {
		return apeBlocksV3900
	}

	return apeBlocksV3800
}

// apeOldBitsPerSample reads the obsolete bit-depth flags. Anything else is 16-bit.
func apeOldBitsPerSample(flags uint16) uint16 {
	switch {
	case flags&apeFormatFlag8Bit != 0:
		return apeBits8
	case flags&apeFormatFlag24Bit != 0:
		return apeBits24
	default:
		return apeBits16
	}
}

// apeTrailingBytes reports how many bytes at the end of the file are an APEv1/v2 tag
// and/or an ID3v1 tag, which are not compressed audio. A tail that does not parse is
// treated as audio so a false match cannot truncate the last frame.
func apeTrailingBytes(file *os.File, size int64) (int64, error) {
	id3, err := apeID3v1Size(file, size)
	if err != nil {
		return 0, err
	}

	tag, err := apeTagSize(file, size-id3)
	if err != nil {
		return 0, err
	}

	return id3 + tag, nil
}

// apeID3v1Size returns id3v1TagLen when the file ends with an ID3v1 tag.
func apeID3v1Size(file *os.File, size int64) (int64, error) {
	if size < id3v1TagLen {
		return 0, nil
	}

	var magic [3]byte

	err := readFileAt(file, size-id3v1TagLen, magic[:])
	if err != nil {
		return 0, err
	}

	if string(magic[:]) != "TAG" {
		return 0, nil
	}

	return id3v1TagLen, nil
}

// apeTagSize returns the total APEv1/v2 tag length ending at end (the file size, or the
// file size minus a trailing ID3v1 tag). The size field includes the footer and excludes
// the header.
func apeTagSize(file *os.File, end int64) (int64, error) {
	if end < apeTagFooterLen {
		return 0, nil
	}

	var footer [apeTagFooterLen]byte

	err := readFileAt(file, end-apeTagFooterLen, footer[:])
	if err != nil {
		return 0, err
	}

	if string(footer[:8]) != "APETAGEX" {
		return 0, nil
	}

	version := binary.LittleEndian.Uint32(footer[8:])
	if version != apeTagVersion1 && version != apeTagVersion2 {
		return 0, nil
	}

	flags := binary.LittleEndian.Uint32(footer[20:])
	if flags&apeTagFlagIsHeader != 0 {
		return 0, nil
	}

	total := int64(binary.LittleEndian.Uint32(footer[12:]))
	if flags&apeTagFlagHasHeader != 0 {
		total += apeTagFooterLen
	}

	if total < apeTagFooterLen || total > end {
		return 0, nil
	}

	return total, nil
}

// readFileAt reads len(buf) bytes at offset.
func readFileAt(file *os.File, offset int64, buf []byte) error {
	_, err := file.ReadAt(buf, offset)
	if err != nil {
		return fmt.Errorf("reading ape file: %w", err)
	}

	return nil
}

// isOldAPE reports whether info came from the legacy header (version < 3.98).
func isOldAPE(info *apeInfo) bool {
	return info.Descriptor.Version < apeNewFormatVersion
}

// apeOldOutputFlags keeps the bit-depth and CRC flags and always asks the decoder to
// synthesize a WAV header. Peak level and the explicit seek-element count are omitted
// because the written header does not carry those fields.
func apeOldOutputFlags(flags uint16) uint16 {
	const keep = apeFormatFlag8Bit | apeFormatFlagCRC | apeFormatFlag24Bit

	return (flags & keep) | apeFormatFlagCreateWAV
}

// buildOldAPETrackContainer serializes a legacy header and seek table for one split track.
func buildOldAPETrackContainer(info *apeInfo, startFrame, endFrame int) (*apeTrackContainer, error) {
	numFrames := endFrame - startFrame + 1
	dataOffset := int64(apeOldHeaderSize) + int64(numFrames)*bytesPerUint32
	layout := layoutAPETrackFrames(info, startFrame, endFrame, dataOffset)

	hdr := apeHeaderOld{
		ID:               info.Descriptor.ID,
		Version:          info.Descriptor.Version,
		CompressionLevel: info.Header.CompressionLevel,
		FormatFlags:      apeOldOutputFlags(info.Header.FormatFlags),
		Channels:         info.Header.Channels,
		SampleRate:       info.Header.SampleRate,
		TotalFrames:      uint32(numFrames),
		FinalFrameBlocks: layout.finalBlocks,
	}
	if hdr.ID == [4]byte{} {
		hdr.ID = [4]byte{'M', 'A', 'C', ' '}
	}

	prefix, err := marshalOldAPEPrefix(&hdr, layout.seekTable)
	if err != nil {
		return nil, err
	}

	return &apeTrackContainer{
		prefix:        prefix,
		trackDataSize: layout.trackDataSize,
		framePadding:  layout.framePadding,
		skipMD5:       true,
	}, nil
}

// marshalOldAPEPrefix serializes a legacy header and its seek table.
func marshalOldAPEPrefix(hdr *apeHeaderOld, seekTable []uint32) ([]byte, error) {
	var buf bytes.Buffer

	err := binary.Write(&buf, binary.LittleEndian, hdr)
	if err != nil {
		return nil, fmt.Errorf("encoding ape header: %w", err)
	}

	err = binary.Write(&buf, binary.LittleEndian, seekTable)
	if err != nil {
		return nil, fmt.Errorf("encoding ape seek table: %w", err)
	}

	return buf.Bytes(), nil
}
