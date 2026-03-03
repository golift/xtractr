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

	// enc.Close() also closes the underlying outFile via io.Closer.
	require.NoError(t, enc.Close(), "closing FLAC encoder")
}

func TestCueParseCueSheet(t *testing.T) {
	t.Parallel()

	assert.True(t, xtractr.IsArchiveFile("test.cue"), ".cue should be recognized as an archive file")
	assert.True(t, xtractr.IsArchiveFile("TEST.CUE"), ".CUE (uppercase) should be recognized")
}

func TestCueExtractCUE(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(3 * 60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	// Open the file ourselves and close it explicitly. flac.Open/ParseFile wrap
	// the reader in bufio.NewReader which prevents Stream.Close from releasing
	// the file handle (the io.Closer interface is lost by the wrapper).
	verifyFile, err := os.Open(flacPath)
	require.NoError(t, err, "opening generated FLAC file for verification")
	verifyStream, err := flac.New(verifyFile)
	require.NoError(t, err, "parsing generated FLAC file")
	assert.Equal(t, uint32(testSampleRate), verifyStream.Info.SampleRate)
	assert.Equal(t, uint8(testNChannels), verifyStream.Info.NChannels)
	require.NoError(t, verifyFile.Close())

	cueContent := strings.Join([]string{
		`PERFORMER "Test Artist"`,
		`TITLE "Test Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "First Song"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Second Song"`,
		`    INDEX 01 01:00:00`,
		`  TRACK 03 AUDIO`,
		`    TITLE "Third Song"`,
		`    INDEX 01 02:00:00`,
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
	require.NoError(t, err, "extracting CUE+FLAC")

	assert.Len(t, files, 3, "expected 3 extracted track files")
	assert.Positive(t, size, "total size should be > 0")
	assert.Len(t, archiveList, 2, "archive list should contain cue and flac files")
	assert.Contains(t, archiveList, cuePath)
	assert.Contains(t, archiveList, flacPath)

	expectedNames := []string{
		"01 - First Song.flac",
		"02 - Second Song.flac",
		"03 - Third Song.flac",
	}

	for idx, expectedName := range expectedNames {
		assert.Equal(t, filepath.Join(outputDir, expectedName), files[idx])
		trackFile, err := os.Open(files[idx])
		require.NoError(t, err, "opening track FLAC file: %s", files[idx])
		trackStream, err := flac.New(trackFile)
		require.NoError(t, err, "parsing track FLAC file: %s", files[idx])
		assert.Equal(t, uint32(testSampleRate), trackStream.Info.SampleRate)
		assert.Equal(t, uint8(testNChannels), trackStream.Info.NChannels)
		assert.Positive(t, trackStream.Info.NSamples, "track should have samples")
		require.NoError(t, trackFile.Close())
	}
}

func TestCueExtractViaExtractFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(30 * testSampleRate)
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
		`    INDEX 01 00:15:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	size, files, archiveList, err := xtractr.ExtractFile(xFile)
	require.NoError(t, err, "ExtractFile with .cue")
	assert.Len(t, files, 2)
	assert.Len(t, archiveList, 2)
	assert.Positive(t, size)
}

func TestCueMissingFlac(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	cueContent := strings.Join([]string{
		`FILE "nonexistent.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, _, _, err := xtractr.ExtractCUE(xFile) //nolint:dogsled
	assert.ErrorIs(t, err, xtractr.ErrAudioNotFound)
}

func TestCueUnsupportedFormat(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	wavPath := filepath.Join(tmpDir, "album.wav")
	require.NoError(t, os.WriteFile(wavPath, []byte("fake"), 0o600))

	cueContent := strings.Join([]string{
		`FILE "album.wav" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, _, _, err := xtractr.ExtractCUE(xFile) //nolint:dogsled
	assert.ErrorIs(t, err, xtractr.ErrUnsupportedAudio)
}

func TestCueTimestampConversion(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(6 * 60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "A"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "B"`,
		`    INDEX 01 05:15:37`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	file1, err := os.Open(files[0])
	require.NoError(t, err)
	stream1, err := flac.New(file1)
	require.NoError(t, err)

	expectedTrack1Samples := uint64((5*60+15)*testSampleRate) + uint64(37*testSampleRate/75)
	assert.Equal(t, expectedTrack1Samples, stream1.Info.NSamples,
		"Track 1 should have the expected number of samples based on CUE timestamp")
	require.NoError(t, file1.Close())

	file2, err := os.Open(files[1])
	require.NoError(t, err)
	stream2, err := flac.New(file2)
	require.NoError(t, err)

	expectedTrack2Samples := totalSamples - expectedTrack1Samples
	assert.Equal(t, expectedTrack2Samples, stream2.Info.NSamples,
		"Track 2 should have the remaining samples")
	require.NoError(t, file2.Close())
}

func TestCueSpecialCharacters(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(10 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`PERFORMER "Test/Artist"`,
		`TITLE "Test: Album?"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Song With / Slash"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Song: With <Special> Chars?"`,
		`    INDEX 01 00:05:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	assert.Equal(t, "01 - Song With - Slash.flac", filepath.Base(files[0]))
	assert.Equal(t, "02 - Song- With Special Chars.flac", filepath.Base(files[1]))
}

func TestCueREMComments(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(10 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`REM GENRE "Rock"`,
		`REM DATE 2024`,
		`REM DISCID 12345678`,
		`PERFORMER "Artist"`,
		`TITLE "Album"`,
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Song"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err)
	assert.Len(t, files, 1)
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
	require.Len(t, files, 2)

	for _, trackPath := range files {
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
	require.Len(t, files, 3, "expected 3 split track files")
	assert.Positive(t, size)
	assert.Len(t, archiveList, 2)

	for _, trackPath := range files {
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

func TestCueSupportedExtensions(t *testing.T) {
	t.Parallel()

	extensions := xtractr.SupportedExtensions()
	found := false

	for _, ext := range extensions {
		if strings.EqualFold(ext, ".cue") {
			found = true
			break
		}
	}

	assert.True(t, found, ".cue should be in supported extensions list")
}
