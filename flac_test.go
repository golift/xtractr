package xtractr_test

import (
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

const (
	testSampleRate    = 44100
	testBitsPerSample = 16
	testNChannels     = 2
	testBlockSize     = 4096
)

// writeTestFLACAudioFrames writes sine-wave audio frames to a FLAC encoder.
// Callers must call enc.Close() after this returns.
func writeTestFLACAudioFrames(t *testing.T, enc *flac.Encoder, totalSamples uint64) {
	t.Helper()

	samplesWritten := uint64(0)
	for samplesWritten < totalSamples {
		blockSize := uint64(testBlockSize)
		if samplesWritten+blockSize > totalSamples {
			blockSize = totalSamples - samplesWritten
		}

		leftSamples := make([]int32, blockSize)
		rightSamples := make([]int32, blockSize)

		for i := range blockSize {
			sampleNum := samplesWritten + i
			val := int32(16000 * math.Sin(2*math.Pi*440*float64(sampleNum)/float64(testSampleRate)))
			leftSamples[i] = val
			rightSamples[i] = val
		}

		audioFrame := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: false,
				BlockSize:         uint16(blockSize),
				SampleRate:        testSampleRate,
				Channels:          frame.ChannelsLR,
				BitsPerSample:     testBitsPerSample,
			},
			Subframes: []*frame.Subframe{
				{
					SubHeader: frame.SubHeader{
						Pred:  frame.PredVerbatim,
						Order: 0,
					},
					Samples:  leftSamples,
					NSamples: int(blockSize),
				},
				{
					SubHeader: frame.SubHeader{
						Pred:  frame.PredVerbatim,
						Order: 0,
					},
					Samples:  rightSamples,
					NSamples: int(blockSize),
				},
			},
		}

		require.NoError(t, enc.WriteFrame(audioFrame), "writing FLAC frame")

		samplesWritten += blockSize
	}
}

// generateTestFLAC creates a FLAC file with a sine wave tone at the given path.
func generateTestFLAC(t *testing.T, path string, totalSamples uint64) {
	t.Helper()

	outFile, err := os.Create(path)
	require.NoError(t, err, "creating test FLAC file")

	info := &meta.StreamInfo{
		BlockSizeMin:  testBlockSize,
		BlockSizeMax:  testBlockSize,
		SampleRate:    testSampleRate,
		NChannels:     testNChannels,
		BitsPerSample: testBitsPerSample,
		NSamples:      totalSamples,
	}

	enc, err := flac.NewEncoder(outFile, info)
	require.NoError(t, err, "creating FLAC encoder")

	writeTestFLACAudioFrames(t, enc, totalSamples)

	// enc.Close() also closes the underlying outFile via io.Closer.
	require.NoError(t, enc.Close(), "closing FLAC encoder")
}

// minimalPNG is a valid 1x1 black pixel PNG (67 bytes) for embedding as front cover in tests.
func minimalPNG(t *testing.T) []byte {
	t.Helper()

	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x01, 0x00, 0x00, 0x00, 0x00, 0x37, 0x6E, 0xF9,
		0x24, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x01, 0x63, 0x60, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x01, 0x73, 0x75, 0x01, 0x18, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
}

// generateTestFLACWithCover creates a FLAC file with an embedded front-cover picture
// and the same sine-wave audio as generateTestFLAC. Used to test that CUE split
// copies the cover into each track.
func generateTestFLACWithCover(t *testing.T, path string, totalSamples uint64) {
	t.Helper()

	outFile, err := os.Create(path)
	require.NoError(t, err, "creating test FLAC file with cover")

	info := &meta.StreamInfo{
		BlockSizeMin:  testBlockSize,
		BlockSizeMax:  testBlockSize,
		SampleRate:    testSampleRate,
		NChannels:     testNChannels,
		BitsPerSample: testBitsPerSample,
		NSamples:      totalSamples,
	}

	coverBlock := &meta.Block{
		Header: meta.Header{Type: meta.TypePicture, Length: 1, IsLast: true},
		Body: &meta.Picture{
			Type:  3, // Cover (front)
			MIME:  "image/png",
			Desc:  "cover",
			Width: 1, Height: 1, Depth: 8, NPalColors: 0,
			Data: minimalPNG(t),
		},
	}

	enc, err := flac.NewEncoder(outFile, info, coverBlock)
	require.NoError(t, err, "creating FLAC encoder with cover")

	writeTestFLACAudioFrames(t, enc, totalSamples)

	require.NoError(t, enc.Close(), "closing FLAC encoder")
}

func TestCueExtractCUE_SplitFLAC_EmbeddedCover(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(2 * 60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLACWithCover(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`PERFORMER "Cover Artist"`,
		`TITLE "Cover Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track A"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Track B"`,
		`    INDEX 01 01:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "extracting CUE+FLAC with cover")

	require.Len(t, files, 4, "expected 2 track files + cover.png + CUE sheet")
	assert.Contains(t, files, filepath.Join(outputDir, "cover.png"), "cover.png should be in extracted file list")

	// Album cover should be written to cover.png in the output directory.
	coverPath := filepath.Join(outputDir, "cover.png")
	coverData, err := os.ReadFile(coverPath)
	require.NoError(t, err, "reading cover.png")
	assert.Equal(t, minimalPNG(t), coverData, "cover.png should match embedded image")

	for _, trackPath := range files {
		if filepath.Ext(trackPath) != ".flac" {
			continue
		}

		trackFile, err := os.Open(trackPath)
		require.NoError(t, err, "opening track: %s", trackPath)
		stream, err := flac.Parse(trackFile)
		require.NoError(t, err, "parsing track: %s", trackPath)

		var frontCover *meta.Picture

		for _, blk := range stream.Blocks {
			if blk.Type != meta.TypePicture {
				continue
			}

			pic, ok := blk.Body.(*meta.Picture)
			if !ok {
				continue
			}

			if pic.Type == 3 {
				frontCover = pic
				break
			}
		}

		require.NotNil(t, frontCover, "track %s should have front-cover Picture block", trackPath)
		assert.Equal(t, "image/png", frontCover.MIME, "cover MIME")
		assert.Equal(t, uint32(1), frontCover.Width, "cover width")
		assert.Equal(t, uint32(1), frontCover.Height, "cover height")
		assert.Equal(t, minimalPNG(t), frontCover.Data, "cover image data should match source")
		require.NoError(t, trackFile.Close())
	}
}

// TestCueVariableBlockSizeConsistency verifies that all frames in every split
// track file use variable-block-size encoding (HasFixedBlockSize=false).
// Mixing fixed- and variable-blocksize frames in one file is invalid FLAC:
// the two modes encode different values in the "frame/sample number" field of
// the frame header, causing decoders such as GStreamer's flacparse to reject
// the file with a stream error.
func TestCueVariableBlockSizeConsistency(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	// Use a block size that will not align perfectly with track boundaries so
	// that the first frame of track 2 is a boundary-split (partial) frame.
	// 44100 samples/s * 1 minute = 2646000 samples; block size 4096 means
	// the track boundary at sample 2646000 falls mid-frame.
	totalSamples := uint64(2 * 60 * testSampleRate) // 2 minutes
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`PERFORMER "Artist"`,
		`TITLE "Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track One"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Track Two"`,
		`    INDEX 01 01:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err)
	require.Len(t, files, 3, "expected 2 track files + CUE sheet")

	for _, trackPath := range files {
		if filepath.Ext(trackPath) != ".flac" {
			continue
		}

		trackFile, err := os.Open(trackPath)
		require.NoError(t, err, "opening track: %s", trackPath)

		stream, err := flac.New(trackFile)
		require.NoError(t, err, "parsing track: %s", trackPath)

		for {
			frm, err := stream.ParseNext()
			if errors.Is(err, io.EOF) {
				break
			}

			require.NoError(t, err, "reading frame from: %s", trackPath)
			assert.False(t, frm.HasFixedBlockSize,
				"frame in %s uses fixed-blocksize encoding; "+
					"all frames must use variable-blocksize (HasFixedBlockSize=false) "+
					"or decoders like GStreamer's flacparse will reject the file",
				filepath.Base(trackPath))
		}

		require.NoError(t, trackFile.Close())
	}
}

// TestCueSplitRealFLAC is an integration test that uses ffmpeg to produce a
// fixed-blocksize FLAC file (the default for all mainstream encoders) and then
// splits it with ExtractCUE.  This is the exact scenario that caused GStreamer's
// flacparse to abort with "streaming stopped, reason error (-5)":
//
//   - ffmpeg-encoded FLACs use HasFixedBlockSize=true in every frame header.
//   - The old buildOutputFrame fast-path returned interior frames as-is
//     (HasFixedBlockSize=true) while boundary-split frames were newly built
//     with HasFixedBlockSize=false.
//   - The mix is invalid FLAC; fixed-blocksize frames encode a frame number
//     while variable-blocksize frames encode a sample position in the same
//     header field, so a decoder reading a mixed file gets garbage positions.
//
// The test is skipped automatically when ffmpeg is not in PATH so it does not
// break CI environments that lack the tool.
func TestCueSplitRealFLAC(t *testing.T) {
	t.Parallel()

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH; skipping real-FLAC integration test")
	}

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")
	flacPath := filepath.Join(tmpDir, "album.flac")

	// Build a 90-second fixed-blocksize FLAC by concatenating three 30-second
	// sine-tone segments at different frequencies (440, 523, 659 Hz).
	// ffmpeg's FLAC encoder always writes fixed-blocksize streams, which is
	// what triggers the bug in the un-patched code.
	cmd := exec.CommandContext(t.Context(), ffmpeg, //nolint:gosec
		"-y",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=523:sample_rate=44100:duration=30",
		"-f", "lavfi", "-i", "sine=frequency=659:sample_rate=44100:duration=30",
		"-filter_complex", "[0][1][2]concat=n=3:v=0:a=1",
		"-c:a", "flac",
		flacPath,
	)

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "ffmpeg failed: %s", out)

	// Verify the source FLAC uses fixed-blocksize encoding — if it doesn't, the
	// test is not exercising the right code path and should be updated.
	requireFixedBlocksizeFLAC(t, flacPath)

	cueContent := strings.Join([]string{
		`PERFORMER "Test Artist"`,
		`TITLE "Test Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "A4 Tone"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "C5 Tone"`,
		`    INDEX 01 00:30:00`,
		`  TRACK 03 AUDIO`,
		`    TITLE "E5 Tone"`,
		`    INDEX 01 01:00:00`,
	}, "\n") + "\n"

	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	size, files, archiveList, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "ExtractCUE failed on ffmpeg-encoded FLAC")
	require.Len(t, files, 4, "expected 3 split track files + CUE sheet")
	assert.Positive(t, size)
	assert.Len(t, archiveList, 2)

	for _, trackPath := range files {
		if filepath.Ext(trackPath) != ".flac" {
			continue
		}

		trackFile, err := os.Open(trackPath)
		require.NoError(t, err)

		stream, err := flac.New(trackFile)
		require.NoError(t, err, "flac.New failed for %s", filepath.Base(trackPath))

		// Every frame in the output must use variable-blocksize encoding.
		// Mixing fixed- and variable-blocksize frames produces an invalid file.
		frameIdx := 0

		for {
			frm, err := stream.ParseNext()
			if errors.Is(err, io.EOF) {
				break
			}

			require.NoError(t, err)
			assert.False(t, frm.HasFixedBlockSize,
				"frame %d in %s uses fixed-blocksize encoding; "+
					"decoders like GStreamer's flacparse will reject the file",
				frameIdx, filepath.Base(trackPath))
			frameIdx++
		}

		require.NoError(t, trackFile.Close())
	}
}

// requireFixedBlocksizeFLAC fails the test if the FLAC at path does not use
// fixed-blocksize encoding.  It is used to assert that our ffmpeg-generated
// source file is actually triggering the code path we want to test.
func requireFixedBlocksizeFLAC(t *testing.T, path string) {
	t.Helper()

	srcFile, err := os.Open(path)
	require.NoError(t, err)

	defer srcFile.Close()

	stream, err := flac.New(srcFile)
	require.NoError(t, err)

	frm, err := stream.ParseNext()
	require.NoError(t, err, "could not read first frame of source FLAC")
	require.True(t, frm.HasFixedBlockSize,
		"source FLAC %s does not use fixed-blocksize encoding; "+
			"the test is not exercising the right code path",
		filepath.Base(path))
}

// generateStereoFLAC writes a stereo FLAC with distinct left/right channels using
// mid/side inter-channel decorrelation, and returns the exact per-channel samples it
// wrote. Real-world FLACs (libFLAC, ffmpeg) routinely use mid/side and left/side
// decorrelation; this exercises the channel-handling path that plain L==R sine-wave
// fixtures (which only ever produce independent L/R frames) never reach.
func generateStereoFLAC(t *testing.T, path string, totalSamples uint64) (left, right []int32) {
	t.Helper()

	outFile, err := os.Create(path)
	require.NoError(t, err, "creating stereo test FLAC file")

	info := &meta.StreamInfo{
		BlockSizeMin:  testBlockSize,
		BlockSizeMax:  testBlockSize,
		SampleRate:    testSampleRate,
		NChannels:     testNChannels,
		BitsPerSample: testBitsPerSample,
		NSamples:      totalSamples,
	}

	enc, err := flac.NewEncoder(outFile, info)
	require.NoError(t, err, "creating stereo FLAC encoder")

	left = make([]int32, 0, totalSamples)
	right = make([]int32, 0, totalSamples)

	samplesWritten := uint64(0)
	for samplesWritten < totalSamples {
		blockSize := uint64(testBlockSize)
		if samplesWritten+blockSize > totalSamples {
			blockSize = totalSamples - samplesWritten
		}

		leftSamples := make([]int32, blockSize)
		rightSamples := make([]int32, blockSize)

		for i := range blockSize {
			n := samplesWritten + i
			// Distinct, correlated-but-not-equal channels so the encoder benefits
			// from mid/side decorrelation (and side = L-R is non-zero).
			leftSamples[i] = int32(15000 * math.Sin(2*math.Pi*440*float64(n)/float64(testSampleRate)))
			rightSamples[i] = int32(12000 * math.Sin(2*math.Pi*443*float64(n)/float64(testSampleRate)))
		}

		left = append(left, leftSamples...)
		right = append(right, rightSamples...)

		audioFrame := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: false,
				BlockSize:         uint16(blockSize),
				SampleRate:        testSampleRate,
				// Force mid/side inter-channel decorrelation; the encoder converts
				// the L/R samples we provide into mid/side on write.
				Channels:      frame.ChannelsMidSide,
				BitsPerSample: testBitsPerSample,
			},
			Subframes: []*frame.Subframe{
				{
					SubHeader: frame.SubHeader{Pred: frame.PredVerbatim, Order: 0},
					Samples:   leftSamples,
					NSamples:  int(blockSize),
				},
				{
					SubHeader: frame.SubHeader{Pred: frame.PredVerbatim, Order: 0},
					Samples:   rightSamples,
					NSamples:  int(blockSize),
				},
			},
		}

		require.NoError(t, enc.WriteFrame(audioFrame), "writing stereo FLAC frame")

		samplesWritten += blockSize
	}

	require.NoError(t, enc.Close(), "closing stereo FLAC encoder")

	return left, right
}

// readAllSamples decodes every frame of a FLAC file and returns the per-channel samples.
func readAllSamples(t *testing.T, path string) (left, right []int32) {
	t.Helper()

	file, err := os.Open(path)
	require.NoError(t, err, "opening FLAC for sample verification: %s", path)

	defer file.Close()

	stream, err := flac.Parse(file)
	require.NoError(t, err, "parsing FLAC for sample verification: %s", path)

	require.Equal(t, uint8(testNChannels), stream.Info.NChannels, "expected stereo: %s", path)

	for {
		frm, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			break
		}

		require.NoError(t, err, "decoding frame from: %s", path)

		left = append(left, frm.Subframes[0].Samples...)
		right = append(right, frm.Subframes[1].Samples...)
	}

	return left, right
}

// TestCueStereoSampleAccuracy verifies that splitting a stereo FLAC encoded with
// mid/side inter-channel decorrelation produces sample-accurate output. The decoder
// already correlates subframes to L/R on Parse, so any extra Correlate() before
// re-encoding double-transforms the samples and corrupts the right channel of every
// non-independent-stereo frame (see Unpackerr/unpackerr#634).
func TestCueStereoSampleAccuracy(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(30 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	srcLeft, srcRight := generateStereoFLAC(t, flacPath, totalSamples)

	// Single track spanning the whole file: the split output must be sample-identical
	// to the source across every frame.
	cueContent := strings.Join([]string{
		`PERFORMER "Artist"`,
		`TITLE "Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Whole"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "extracting stereo CUE+FLAC")

	trackPath := filepath.Join(outputDir, "01 - Whole.flac")
	assert.Contains(t, files, trackPath)

	gotLeft, gotRight := readAllSamples(t, trackPath)

	require.Len(t, gotLeft, len(srcLeft), "left channel sample count")
	require.Len(t, gotRight, len(srcRight), "right channel sample count")
	assertSamplesEqual(t, "left", srcLeft, gotLeft)
	assertSamplesEqual(t, "right", srcRight, gotRight)
}

// assertSamplesEqual compares two sample slices and fails with the first mismatch
// (and a count) instead of dumping millions of values like assert.Equal would.
func assertSamplesEqual(t *testing.T, channel string, want, got []int32) {
	t.Helper()

	mismatches := 0
	firstIdx := -1

	for i := range want {
		if want[i] != got[i] {
			if firstIdx < 0 {
				firstIdx = i
			}

			mismatches++
		}
	}

	if mismatches > 0 {
		t.Errorf("%s channel: %d/%d samples differ; first mismatch at index %d: want %d, got %d",
			channel, mismatches, len(want), firstIdx, want[firstIdx], got[firstIdx])
	}
}
