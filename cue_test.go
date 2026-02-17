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

		for i := uint64(0); i < blockSize; i++ {
			sampleNum := samplesWritten + i
			val := int32(16000 * math.Sin(2*math.Pi*440*float64(sampleNum)/float64(testSampleRate)))
			leftSamples[i] = val
			rightSamples[i] = val
		}

		f := &frame.Frame{
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

		require.NoError(t, enc.WriteFrame(f), "writing FLAC frame")
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
	stream.Close()

	cueContent := "PERFORMER \"Test Artist\"\nTITLE \"Test Album\"\nFILE \"album.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"First Song\"\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    TITLE \"Second Song\"\n    INDEX 01 01:00:00\n  TRACK 03 AUDIO\n    TITLE \"Third Song\"\n    INDEX 01 02:00:00\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
		DirMode:   0o755,
	}

	size, files, archiveList, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "extracting CUE+FLAC")

	assert.Len(t, files, 3, "expected 3 extracted track files")
	assert.Greater(t, size, uint64(0), "total size should be > 0")
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
		assert.Greater(t, trackStream.Info.NSamples, uint64(0), "track should have samples")
		trackStream.Close()
	}
}

func TestCueExtractViaExtractFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(30 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := "PERFORMER \"Artist\"\nTITLE \"Album\"\nFILE \"album.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Track One\"\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    TITLE \"Track Two\"\n    INDEX 01 00:15:00\n"
	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
		DirMode:   0o755,
	}

	size, files, archiveList, err := xtractr.ExtractFile(xFile)
	require.NoError(t, err, "ExtractFile with .cue")
	assert.Len(t, files, 2)
	assert.Len(t, archiveList, 2)
	assert.Greater(t, size, uint64(0))
}

func TestCueMissingFlac(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	cueContent := "FILE \"nonexistent.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Track\"\n    INDEX 01 00:00:00\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
		DirMode:   0o755,
	}

	_, _, _, err := xtractr.ExtractCUE(xFile)
	assert.ErrorIs(t, err, xtractr.ErrAudioNotFound)
}

func TestCueUnsupportedFormat(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	wavPath := filepath.Join(tmpDir, "album.wav")
	require.NoError(t, os.WriteFile(wavPath, []byte("fake"), 0o644))

	cueContent := "FILE \"album.wav\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Track\"\n    INDEX 01 00:00:00\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
		DirMode:   0o755,
	}

	_, _, _, err := xtractr.ExtractCUE(xFile)
	assert.ErrorIs(t, err, xtractr.ErrUnsupportedAudio)
}

func TestCueTimestampConversion(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(6 * 60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := "FILE \"album.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"A\"\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    TITLE \"B\"\n    INDEX 01 05:15:37\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
		DirMode:   0o755,
	}

	_, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err)
	assert.Len(t, files, 2)

	stream1, err := flac.Open(files[0])
	require.NoError(t, err)
	defer stream1.Close()

	expectedTrack1Samples := uint64((5*60+15)*testSampleRate) + uint64(37*testSampleRate/75)
	assert.Equal(t, expectedTrack1Samples, stream1.Info.NSamples,
		"Track 1 should have the expected number of samples based on CUE timestamp")

	stream2, err := flac.Open(files[1])
	require.NoError(t, err)
	defer stream2.Close()

	expectedTrack2Samples := totalSamples - expectedTrack1Samples
	assert.Equal(t, expectedTrack2Samples, stream2.Info.NSamples,
		"Track 2 should have the remaining samples")
}

func TestCueSpecialCharacters(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(10 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := "PERFORMER \"Test/Artist\"\nTITLE \"Test: Album?\"\nFILE \"album.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Song With / Slash\"\n    INDEX 01 00:00:00\n  TRACK 02 AUDIO\n    TITLE \"Song: With <Special> Chars?\"\n    INDEX 01 00:05:00\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
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

	cueContent := "REM GENRE \"Rock\"\nREM DATE 2024\nREM DISCID 12345678\nPERFORMER \"Artist\"\nTITLE \"Album\"\nFILE \"album.flac\" WAVE\n  TRACK 01 AUDIO\n    TITLE \"Song\"\n    INDEX 01 00:00:00\n"
	cuePath := filepath.Join(tmpDir, "test.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o644))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o644,
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
