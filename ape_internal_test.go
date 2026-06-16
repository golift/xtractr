package xtractr

import (
	"bytes"
	"crypto/md5" //nolint:gosec // MD5 is the APE file integrity hash, not security.
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test fixture constants. APE blocks are per-channel samples; with a sample rate equal to
// the blocks-per-frame, one second of CUE time advances exactly one frame, which keeps the
// timestamp-to-frame math easy to reason about in the tests.
const (
	testAPEVersion          = 3990
	testAPEBlocksPerFrame   = 100
	testAPEFinalFrameBlocks = 50
	testAPESampleRate       = 100
	testAPEBitsPerSample    = 16
	testAPEChannels         = 2
	testAPEFrameLen         = 7 // Deliberately not a multiple of 4 so realignment is exercised.
)

// syntheticAPE builds a structurally valid "new format" (>= 3.98) APE file in memory. The
// frame payloads are arbitrary bytes (not real Monkey's Audio) which is sufficient to test
// container parsing, seek-table handling and the byte-level split/realignment plumbing.
type syntheticAPE struct {
	version          uint16
	blocksPerFrame   uint32
	finalFrameBlocks uint32
	bitsPerSample    uint16
	channels         uint16
	sampleRate       uint32
	junk             []byte   // Bytes before the descriptor (ID3v2 tag, etc.).
	frames           [][]byte // Compressed frame payloads.
	seekTableEntries int      // 0 means "one entry per frame" (the valid case).
}

// defaultSyntheticAPE returns a builder with sane defaults and the given frame payloads.
func defaultSyntheticAPE(frames [][]byte) *syntheticAPE {
	return &syntheticAPE{
		version:          testAPEVersion,
		blocksPerFrame:   testAPEBlocksPerFrame,
		finalFrameBlocks: testAPEFinalFrameBlocks,
		bitsPerSample:    testAPEBitsPerSample,
		channels:         testAPEChannels,
		sampleRate:       testAPESampleRate,
		frames:           frames,
	}
}

// equalFrames returns count frames each filled with a distinct, repeating byte pattern.
func equalFrames(count int) [][]byte {
	frames := make([][]byte, count)

	for idx := range frames {
		frame := make([]byte, testAPEFrameLen)
		for j := range frame {
			frame[j] = byte((idx+1)*31 + j*7 + 1)
		}

		frames[idx] = frame
	}

	return frames
}

// bytes serializes the synthetic APE to a byte slice.
func (s *syntheticAPE) bytes() []byte {
	totalFrames := len(s.frames)

	entries := s.seekTableEntries
	if entries == 0 {
		entries = totalFrames
	}

	seekTableBytes := entries * bytesPerUint32
	dataStart := apeDescriptorSize + apeHeaderSize + seekTableBytes

	seek := make([]uint32, totalFrames)
	frameData := []byte{}
	offset := dataStart

	for idx, frame := range s.frames {
		seek[idx] = uint32(offset)
		offset += len(frame)
		frameData = append(frameData, frame...)
	}

	desc := apeDescriptor{
		ID:              [4]byte{'M', 'A', 'C', ' '},
		Version:         s.version,
		DescriptorBytes: apeDescriptorSize,
		HeaderBytes:     apeHeaderSize,
		SeekTableBytes:  uint32(seekTableBytes),
		FrameDataBytes:  uint32(len(frameData)),
	}

	hdr := apeHeader{
		FormatFlags:      apeFormatFlagCreateWAV,
		BlocksPerFrame:   s.blocksPerFrame,
		FinalFrameBlocks: s.finalFrameBlocks,
		TotalFrames:      uint32(totalFrames),
		BitsPerSample:    s.bitsPerSample,
		Channels:         s.channels,
		SampleRate:       s.sampleRate,
	}

	var buf bytes.Buffer

	buf.Write(s.junk)
	_ = binary.Write(&buf, binary.LittleEndian, &desc)
	_ = binary.Write(&buf, binary.LittleEndian, &hdr)
	_ = binary.Write(&buf, binary.LittleEndian, seek)
	buf.Write(frameData)

	return buf.Bytes()
}

// writeTo writes the synthetic APE to a temp file and returns its path.
func (s *syntheticAPE) writeTo(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "album.ape")
	require.NoError(t, os.WriteFile(path, s.bytes(), 0o600))

	return path
}

func TestIsAPEMagic(t *testing.T) {
	t.Parallel()

	assert.True(t, isAPEMagic([4]byte{'M', 'A', 'C', ' '}))
	assert.True(t, isAPEMagic([4]byte{'M', 'A', 'C', 'F'}))
	assert.False(t, isAPEMagic([4]byte{'M', 'A', 'C', 'X'}))
	assert.False(t, isAPEMagic([4]byte{'f', 'L', 'a', 'C'}))
}

func TestRoundUpToUint32(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, roundUpToUint32(0))
	assert.Equal(t, 4, roundUpToUint32(1))
	assert.Equal(t, 4, roundUpToUint32(4))
	assert.Equal(t, 8, roundUpToUint32(5))
	assert.Equal(t, 12, roundUpToUint32(9))
}

func TestByteSwapUint32s(t *testing.T) {
	t.Parallel()

	// Two whole words plus 2 trailing bytes that must be left untouched.
	buf := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	byteSwapUint32s(buf)
	assert.Equal(t, []byte{4, 3, 2, 1, 8, 7, 6, 5, 9, 10}, buf)

	// Swapping twice restores the original (for whole words).
	whole := []byte{10, 20, 30, 40, 50, 60, 70, 80}
	original := append([]byte(nil), whole...)
	byteSwapUint32s(whole)
	byteSwapUint32s(whole)
	assert.Equal(t, original, whole)
}

// logicalStream reproduces how the MAC decoder reads the bitstream: it loads little-endian
// uint32 words and consumes them most-significant-byte first, so the logical byte order is
// the per-word reverse of the file bytes. This is an independent reference implementation
// used to validate realignFrameData.
func logicalStream(fileBytes []byte) []byte {
	padded := make([]byte, roundUpToUint32(len(fileBytes)))
	copy(padded, fileBytes)

	out := make([]byte, len(padded))
	for i := 0; i+3 < len(padded); i += bytesPerUint32 {
		out[i], out[i+1], out[i+2], out[i+3] = padded[i+3], padded[i+2], padded[i+1], padded[i]
	}

	return out
}

// decodeFrame returns the logical bytes the decoder would read starting at a word-aligned
// position after skipping skip bytes (the seek remainder).
func decodeFrame(fileBytes []byte, skip int) []byte {
	return logicalStream(fileBytes)[skip:]
}

// TestRealignFrameDataMatchesDecoderSeek is the most important test: it proves that a track
// realigned to start at remainder 0 decodes to the exact same logical bytes the original
// decoder would have read at the track's real (non-zero) remainder. This is what guarantees
// the per-frame CRCs still pass after splitting.
func TestRealignFrameDataMatchesDecoderSeek(t *testing.T) {
	t.Parallel()

	const (
		dataLen = 41 // arbitrary, not a multiple of 4
		maxPad  = 4  // remainders 0..3
	)

	for pad := range maxPad {
		raw := make([]byte, roundUpToUint32(pad+dataLen+apeTailPadding))
		for i := range raw {
			raw[i] = byte(i*13 + 3)
		}

		realigned := realignFrameData(raw, pad)

		want := decodeFrame(raw, pad)    // What the decoder reads from the source (skip pad).
		got := decodeFrame(realigned, 0) // What the decoder reads from our output (skip 0).

		require.GreaterOrEqual(t, len(want), dataLen)
		require.GreaterOrEqual(t, len(got), dataLen)
		assert.Equal(t, want[:dataLen], got[:dataLen], "realignment mismatch for pad=%d", pad)
	}
}

func TestRealignFrameDataPadZeroIsPassthrough(t *testing.T) {
	t.Parallel()

	data := []byte{1, 2, 3, 4, 5}
	assert.Equal(t, data, realignFrameData(data, 0))
}

// TestStreamRealignMatchesInMemory verifies the streaming realigner used in production
// produces byte-identical output to the in-memory reference implementation across many pad
// values and data sizes, including sizes that aren't a multiple of the 4-byte word.
func TestStreamRealignMatchesInMemory(t *testing.T) {
	t.Parallel()

	const maxPad = 4

	// Sizes include values larger than apeCopyBuf to exercise the multi-read carry path.
	sizes := []int{1, 3, 4, 7, 16, 33, 4095, 4096, 4097, 100000, apeCopyBuf + 1, 2*apeCopyBuf + 7}

	for pad := 1; pad < maxPad; pad++ {
		for _, dataSize := range sizes {
			// Source bytes: pad prefix + dataSize of frame data + a few trailing bytes.
			src := make([]byte, roundUpToUint32(pad+dataSize+8))
			for i := range src {
				src[i] = byte(i*37 + pad*5 + 1)
			}

			want := realignFrameData(src, pad)[:dataSize]

			var got bytes.Buffer
			require.NoError(t, streamRealignFrameData(&got, bytes.NewReader(src), pad, int64(dataSize)))

			assert.Equal(t, want, got.Bytes(), "pad=%d dataSize=%d", pad, dataSize)
		}
	}
}

// TestStreamRealignExactEOF covers the last-track case where the source ends exactly at the
// end of the frame data (no trailing bytes), which must still produce the full output.
func TestStreamRealignExactEOF(t *testing.T) {
	t.Parallel()

	const (
		pad      = 2
		dataSize = 30
	)

	src := make([]byte, pad+dataSize) // nothing after the frame data
	for i := range src {
		src[i] = byte(i + 1)
	}

	want := realignFrameData(src, pad)[:dataSize]

	var got bytes.Buffer
	require.NoError(t, streamRealignFrameData(&got, bytes.NewReader(src), pad, int64(dataSize)))
	assert.Equal(t, want, got.Bytes())
}

// TestStreamRealignTruncatedSource ensures a source shorter than the requested track errors
// rather than emitting silently padded data.
func TestStreamRealignTruncatedSource(t *testing.T) {
	t.Parallel()

	src := make([]byte, 10)

	var got bytes.Buffer

	err := streamRealignFrameData(&got, bytes.NewReader(src), 2, 1000)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

// TestCopyFrameDataFrameEndsAtEOF verifies copyFrameData succeeds when the final frame's
// data ends exactly at end-of-file (io.CopyN must not report this as an error).
func TestCopyFrameDataFrameEndsAtEOF(t *testing.T) {
	t.Parallel()

	// Two frames laid out back to back, file ends exactly after the second frame.
	frame0 := []byte{1, 2, 3, 4}
	frame1 := []byte{10, 20, 30, 40, 50, 60}

	info := &apeInfo{
		Header:    apeHeader{TotalFrames: 2},
		SeekTable: []int64{0, int64(len(frame0))},
		FrameData: uint64(len(frame0) + len(frame1)),
	}

	tmp := filepath.Join(t.TempDir(), "frames.bin")
	require.NoError(t, os.WriteFile(tmp, append(append([]byte{}, frame0...), frame1...), 0o600))

	srcFile, err := os.Open(tmp)
	require.NoError(t, err)

	defer srcFile.Close()

	var out bytes.Buffer

	require.NoError(t, copyFrameData(&out, srcFile, info, 0, 1))
	assert.Equal(t, append(append([]byte{}, frame0...), frame1...), out.Bytes())
}

func TestParseAPEBasic(t *testing.T) {
	t.Parallel()

	frames := equalFrames(4)
	path := defaultSyntheticAPE(frames).writeTo(t)

	info, err := parseAPE(path)
	require.NoError(t, err)

	assert.Equal(t, int64(0), info.JunkBytes)
	assert.Equal(t, uint16(testAPEVersion), info.Descriptor.Version)
	assert.Equal(t, uint32(4), info.Header.TotalFrames)
	assert.Equal(t, uint32(testAPEBlocksPerFrame), info.Header.BlocksPerFrame)
	assert.Equal(t, uint32(testAPEFinalFrameBlocks), info.Header.FinalFrameBlocks)
	assert.Equal(t, uint32(testAPESampleRate), info.Header.SampleRate)
	assert.Len(t, info.SeekTable, 4)
	assert.Equal(t, uint64(4*testAPEFrameLen), info.FrameData)

	// TotalBlocks = (frames-1)*blocksPerFrame + finalFrameBlocks.
	assert.Equal(t, uint64(3*testAPEBlocksPerFrame+testAPEFinalFrameBlocks), info.TotalBlocks)

	// Seek table is descriptor-relative; first frame sits right after the seek table.
	dataStart := int64(apeDescriptorSize + apeHeaderSize + 4*bytesPerUint32)
	assert.Equal(t, dataStart, info.SeekTable[0])
	assert.Equal(t, dataStart+testAPEFrameLen, info.SeekTable[1])
}

func TestParseAPESkipsID3v2Tag(t *testing.T) {
	t.Parallel()

	// A minimal ID3v2 header declaring a 32-byte (sync-safe) tag body, no footer.
	const tagBody = 32

	id3 := make([]byte, id3v2HeaderLen+tagBody)
	id3[0], id3[1], id3[2] = 'I', 'D', '3'
	id3[3], id3[4] = 0x04, 0x00 // version
	id3[5] = 0x00               // flags: no footer
	id3[9] = tagBody            // size (low byte; sync-safe)

	builder := defaultSyntheticAPE(equalFrames(2))
	builder.junk = id3

	info, err := parseAPE(builder.writeTo(t))
	require.NoError(t, err)

	assert.Equal(t, int64(id3v2HeaderLen+tagBody), info.JunkBytes)
	assert.Equal(t, uint32(2), info.Header.TotalFrames)
}

func TestParseAPEScansPastArbitraryJunk(t *testing.T) {
	t.Parallel()

	builder := defaultSyntheticAPE(equalFrames(2))
	builder.junk = []byte("garbage-before-the-descriptor")

	info, err := parseAPE(builder.writeTo(t))
	require.NoError(t, err)

	assert.Equal(t, int64(len(builder.junk)), info.JunkBytes)
}

func TestParseAPESeekTable4GBOverflow(t *testing.T) {
	t.Parallel()

	// Craft a seek table whose raw uint32 values wrap (decrease), which signals a >4GB file.
	// The parser must add 2^32 each time a value is smaller than its predecessor.
	var buf bytes.Buffer

	desc := apeDescriptor{
		ID:              [4]byte{'M', 'A', 'C', ' '},
		Version:         testAPEVersion,
		DescriptorBytes: apeDescriptorSize,
		HeaderBytes:     apeHeaderSize,
		SeekTableBytes:  3 * bytesPerUint32,
		FrameDataBytes:  30,
	}
	hdr := apeHeader{
		BlocksPerFrame: testAPEBlocksPerFrame,
		TotalFrames:    3,
		SampleRate:     testAPESampleRate,
	}
	rawSeek := []uint32{0xFFFFFF00, 0x00000010, 0x00000020} // wraps once after entry 0.

	_ = binary.Write(&buf, binary.LittleEndian, &desc)
	_ = binary.Write(&buf, binary.LittleEndian, &hdr)
	_ = binary.Write(&buf, binary.LittleEndian, rawSeek)
	buf.Write(make([]byte, 64)) // frame data padding

	path := filepath.Join(t.TempDir(), "big.ape")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	info, err := parseAPE(path)
	require.NoError(t, err)

	assert.Equal(t, int64(0xFFFFFF00), info.SeekTable[0])
	assert.Equal(t, int64(0x100000010), info.SeekTable[1]) // +2^32 applied
	assert.Equal(t, int64(0x100000020), info.SeekTable[2])
}

func TestParseAPEErrors(t *testing.T) {
	t.Parallel()

	t.Run("old version", func(t *testing.T) {
		t.Parallel()

		builder := defaultSyntheticAPE(equalFrames(2))
		builder.version = 3970

		_, err := parseAPE(builder.writeTo(t))
		require.ErrorIs(t, err, ErrAPEOldVersion)
	})

	t.Run("no frames", func(t *testing.T) {
		t.Parallel()

		builder := defaultSyntheticAPE(nil)

		_, err := parseAPE(builder.writeTo(t))
		require.ErrorIs(t, err, ErrAPENoFrames)
	})

	t.Run("short seek table", func(t *testing.T) {
		t.Parallel()

		builder := defaultSyntheticAPE(equalFrames(4))
		builder.seekTableEntries = 2 // fewer entries than frames

		_, err := parseAPE(builder.writeTo(t))
		require.ErrorIs(t, err, ErrAPESeekTable)
	})

	t.Run("not an ape file", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "notape.bin")
		require.NoError(t, os.WriteFile(path, []byte("this is not a monkey audio file"), 0o600))

		_, err := parseAPE(path)
		require.ErrorIs(t, err, ErrAPENotFound)
	})
}

func TestAPEFrameDataSize(t *testing.T) {
	t.Parallel()

	info := &apeInfo{
		Header:    apeHeader{TotalFrames: 3},
		SeekTable: []int64{100, 110, 135},
		FrameData: 60, // total compressed bytes across all frames
	}

	assert.Equal(t, int64(10), apeFrameDataSize(info, 0)) // 110 - 100
	assert.Equal(t, int64(25), apeFrameDataSize(info, 1)) // 135 - 110
	// Last frame: FrameData - (seek[2] - seek[0]) = 60 - 35.
	assert.Equal(t, int64(25), apeFrameDataSize(info, 2))
}

func TestBuildAPETrackContainer(t *testing.T) {
	t.Parallel()

	frames := equalFrames(4)
	path := defaultSyntheticAPE(frames).writeTo(t)

	info, err := parseAPE(path)
	require.NoError(t, err)

	// A container for frames 1..2 (interior frames; not the source's final frame).
	con, err := buildAPETrackContainer(info, 1, 2)
	require.NoError(t, err)

	assert.Equal(t, uint32(2), con.descriptor.SeekTableBytes/bytesPerUint32)
	assert.Equal(t, int64(2*testAPEFrameLen), con.trackDataSize)
	assert.Equal(t, uint32(2*testAPEFrameLen), con.descriptor.FrameDataBytes)
	assert.Equal(t, [4]byte{'M', 'A', 'C', ' '}, con.descriptor.ID)

	// CREATE_WAV_HEADER flag must be set and there must be no stored header data.
	require.Len(t, con.headerBytes, apeHeaderSize)
	flags := binary.LittleEndian.Uint16(con.headerBytes[2:])
	assert.NotZero(t, flags&apeFormatFlagCreateWAV)
	assert.Zero(t, con.descriptor.HeaderDataBytes)

	// Interior track keeps the full per-frame block count (not the source's final block count).
	finalFrameBlocks := binary.LittleEndian.Uint32(con.headerBytes[8:])
	assert.Equal(t, uint32(testAPEBlocksPerFrame), finalFrameBlocks)

	// New seek table starts immediately after descriptor + header + seek table.
	wantFirstOffset := uint32(apeDescriptorSize + apeHeaderSize + 2*bytesPerUint32)
	assert.Equal(t, wantFirstOffset, binary.LittleEndian.Uint32(con.seekTableBytes[0:]))
}

func TestBuildAPETrackContainerFinalFrame(t *testing.T) {
	t.Parallel()

	info, err := parseAPE(defaultSyntheticAPE(equalFrames(4)).writeTo(t))
	require.NoError(t, err)

	// A track ending on the source's final frame must carry the short final-frame block count.
	con, err := buildAPETrackContainer(info, 2, 3)
	require.NoError(t, err)

	finalFrameBlocks := binary.LittleEndian.Uint32(con.headerBytes[8:])
	assert.Equal(t, uint32(testAPEFinalFrameBlocks), finalFrameBlocks)
}

// apeFileMD5 reads a complete (junk-free) APE file's descriptor and recomputes the file MD5
// the way the Monkey's Audio encoder does for a file with no WAV header or terminating data:
// md5(frame data + APE header + seek table). It returns the stored and recomputed digests.
func apeFileMD5(t *testing.T, fileBytes []byte) (stored, computed []byte) {
	t.Helper()

	var desc apeDescriptor
	require.NoError(t, binary.Read(bytes.NewReader(fileBytes), binary.LittleEndian, &desc))

	headerOffset := int(desc.DescriptorBytes)
	seekOffset := headerOffset + int(desc.HeaderBytes)
	dataOffset := seekOffset + int(desc.SeekTableBytes)

	header := fileBytes[headerOffset:seekOffset]
	seekTable := fileBytes[seekOffset:dataOffset]
	frameData := fileBytes[dataOffset : dataOffset+int(desc.FrameDataBytes)]

	hash := md5.New() //nolint:gosec // matches the APE file format hash.
	_, _ = hash.Write(frameData)
	_, _ = hash.Write(header)
	_, _ = hash.Write(seekTable)

	return desc.FileMD5[:], hash.Sum(nil)
}

// TestSplitAPEEndToEnd splits a synthetic APE into two tracks and verifies the output is
// structurally valid, has correct per-track metadata, a correct file MD5, and that the
// word-aligned first track is copied verbatim while the misaligned track is realigned.
func TestSplitAPEEndToEnd(t *testing.T) {
	t.Parallel()

	frames := equalFrames(4)
	srcBytes := defaultSyntheticAPE(frames).bytes()

	srcPath := filepath.Join(t.TempDir(), "album.ape")
	require.NoError(t, os.WriteFile(srcPath, srcBytes, 0o600))

	outDir := t.TempDir()
	xFile := &XFile{OutputDir: outDir, FileMode: 0o600, DirMode: 0o700}

	// Track 1 starts at 0s (frame 0); track 2 starts at 2s -> 200 samples -> frame 2.
	cue := &CueSheet{Tracks: []CueTrack{
		{Number: 1, Title: "First"},
		{Number: 2, Title: "Second"},
	}}
	timestamps := []cueTimestamp{{}, {seconds: 2}}

	size, files, err := splitAPE(xFile, srcPath, cue, timestamps)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Positive(t, size)

	for _, trackPath := range files {
		info, parseErr := parseAPE(trackPath)
		require.NoError(t, parseErr, "split track must be a parseable APE file")
		assert.Equal(t, uint32(2), info.Header.TotalFrames, "each track spans two source frames")

		trackBytes, readErr := os.ReadFile(trackPath)
		require.NoError(t, readErr)

		stored, computed := apeFileMD5(t, trackBytes)
		assert.Equal(t, computed, stored, "stored file MD5 must match recomputed digest")
		assert.NotEqual(t, make([]byte, apeFileMD5Size), stored, "MD5 must be populated, not zero")
	}

	// Track 1 starts on frame 0 (remainder 0) so its frame data is copied verbatim.
	track1, err := os.ReadFile(files[0])
	require.NoError(t, err)

	var desc apeDescriptor
	require.NoError(t, binary.Read(bytes.NewReader(track1), binary.LittleEndian, &desc))

	dataOffset := int(desc.DescriptorBytes + desc.HeaderBytes + desc.SeekTableBytes)
	gotFrameData := track1[dataOffset : dataOffset+int(desc.FrameDataBytes)]

	wantFrameData := frames[0]
	wantFrameData = append(wantFrameData, frames[1]...)
	assert.Equal(t, wantFrameData, gotFrameData, "aligned track frame data should be copied verbatim")
}

func TestSplitAPESingleTrack(t *testing.T) {
	t.Parallel()

	frames := equalFrames(3)
	srcPath := defaultSyntheticAPE(frames).writeTo(t)

	xFile := &XFile{OutputDir: t.TempDir(), FileMode: 0o600, DirMode: 0o700}
	cue := &CueSheet{Tracks: []CueTrack{{Number: 1, Title: "Only"}}}

	_, files, err := splitAPE(xFile, srcPath, cue, []cueTimestamp{{}})
	require.NoError(t, err)
	require.Len(t, files, 1)

	info, err := parseAPE(files[0])
	require.NoError(t, err)
	assert.Equal(t, uint32(3), info.Header.TotalFrames, "single track should contain every frame")
}

// writeTwoTrackAPECue writes a 4-frame synthetic .ape (named apeName) and an album.cue that
// references fileLine into dir, then returns the .cue path. With the test fixture's sample
// rate (100) the second track's INDEX 00:02:00 (200 samples) lands on frame 2.
func writeTwoTrackAPECue(t *testing.T, dir, apeName, fileLine string) string {
	t.Helper()

	apeBytes := defaultSyntheticAPE(equalFrames(4)).bytes()
	require.NoError(t, os.WriteFile(filepath.Join(dir, apeName), apeBytes, 0o600))

	cueContent := strings.Join([]string{
		`PERFORMER "Test Artist"`,
		`TITLE "Test Album"`,
		`FILE "` + fileLine + `" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "First Song"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Second Song"`,
		`    INDEX 01 00:02:00`,
	}, "\n") + "\n"

	cuePath := filepath.Join(dir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	return cuePath
}

// assertExtractCUEAPE runs ExtractCUE and checks that it produced two parseable .ape tracks
// plus the copied CUE sheet, and that the archive list references the source CUE and audio file.
func assertExtractCUEAPE(t *testing.T, cuePath, audioPath, outDir string) {
	t.Helper()

	xFile := &XFile{FilePath: cuePath, OutputDir: outDir, FileMode: 0o600, DirMode: 0o755}

	size, files, archives, err := ExtractCUE(xFile)
	require.NoError(t, err, "ExtractCUE with .ape audio")
	assert.Positive(t, size)
	require.Len(t, files, 3, "expected 2 track files + copied CUE sheet")
	require.Len(t, archives, 2, "archive list should contain the cue and ape files")
	assert.Contains(t, archives, cuePath)
	assert.Contains(t, archives, audioPath)

	for _, name := range []string{"01 - First Song.ape", "02 - Second Song.ape"} {
		trackPath := filepath.Join(outDir, name)
		assert.Contains(t, files, trackPath)

		info, parseErr := parseAPE(trackPath)
		require.NoError(t, parseErr, "split track must be a parseable APE file: %s", name)
		assert.Equal(t, uint32(2), info.Header.TotalFrames, "each track spans two source frames")
	}
}

// TestExtractCUEAPEEndToEnd exercises the public ExtractCUE entrypoint dispatching to splitAPE
// when the CUE FILE line points directly at an existing .ape file.
func TestExtractCUEAPEEndToEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cuePath := writeTwoTrackAPECue(t, dir, "album.ape", "album.ape")

	assertExtractCUEAPE(t, cuePath, filepath.Join(dir, "album.ape"), filepath.Join(dir, "output"))
}

// TestExtractCUEAPEWavFallback covers resolveCueAudioPath: the CUE references album.wav but
// only album.ape exists on disk, so the .wav -> .ape fallback must find it.
func TestExtractCUEAPEWavFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cuePath := writeTwoTrackAPECue(t, dir, "album.ape", "album.wav")

	assertExtractCUEAPE(t, cuePath, filepath.Join(dir, "album.ape"), filepath.Join(dir, "output"))
}

// TestExtractCUEAPEBasenameFallback covers the same-basename fallback: the CUE FILE line names
// a file that does not exist (and is not a .wav), so resolution falls back to the .ape that
// shares the CUE file's base name (album.cue -> album.ape).
func TestExtractCUEAPEBasenameFallback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cuePath := writeTwoTrackAPECue(t, dir, "album.ape", "totally-different-name.cda")

	assertExtractCUEAPE(t, cuePath, filepath.Join(dir, "album.ape"), filepath.Join(dir, "output"))
}

// TestReadAPESeekTableRejectsHugeTotalFrames verifies the seek-table reader refuses a crafted
// header whose TotalFrames (and SeekTableBytes) claim far more entries than the file can hold,
// rather than attempting a massive allocation.
func TestReadAPESeekTableRejectsHugeTotalFrames(t *testing.T) {
	t.Parallel()

	raw := defaultSyntheticAPE(equalFrames(4)).bytes()

	const (
		descSeekTableBytesOffset = 16                     // apeDescriptor.SeekTableBytes
		headerTotalFramesOffset  = apeDescriptorSize + 12 // apeHeader.TotalFrames
		huge                     = uint32(0x10000000)     // 268M entries -> ~2GB if allocated
	)

	binary.LittleEndian.PutUint32(raw[descSeekTableBytesOffset:], huge*bytesPerUint32)
	binary.LittleEndian.PutUint32(raw[headerTotalFramesOffset:], huge)

	path := filepath.Join(t.TempDir(), "huge.ape")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	_, err := parseAPE(path)
	require.ErrorIs(t, err, ErrAPESeekTable)
}
