package xtractr_test

import (
	"math"
	"os"
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

	defer outFile.Close()

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

	stream, err := flac.Open(flacPath)
	require.NoError(t, err, "opening generated FLAC file")
	assert.Equal(t, uint32(testSampleRate), stream.Info.SampleRate)
	assert.Equal(t, uint8(testNChannels), stream.Info.NChannels)
	require.NoError(t, stream.Close())

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
		trackStream, err := flac.Open(files[idx])
		require.NoError(t, err, "opening track FLAC file: %s", files[idx])
		assert.Equal(t, uint32(testSampleRate), trackStream.Info.SampleRate)
		assert.Equal(t, uint8(testNChannels), trackStream.Info.NChannels)
		assert.Positive(t, trackStream.Info.NSamples, "track should have samples")
		require.NoError(t, trackStream.Close())
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

	stream1, err := flac.Open(files[0])
	require.NoError(t, err)

	expectedTrack1Samples := uint64((5*60+15)*testSampleRate) + uint64(37*testSampleRate/75)
	assert.Equal(t, expectedTrack1Samples, stream1.Info.NSamples,
		"Track 1 should have the expected number of samples based on CUE timestamp")
	require.NoError(t, stream1.Close())

	stream2, err := flac.Open(files[1])
	require.NoError(t, err)

	expectedTrack2Samples := totalSamples - expectedTrack1Samples
	assert.Equal(t, expectedTrack2Samples, stream2.Info.NSamples,
		"Track 2 should have the remaining samples")
	require.NoError(t, stream2.Close())
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
