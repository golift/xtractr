package xtractr_test

import (
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

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

	assert.Len(t, files, 4, "expected 3 track files + CUE sheet")
	assert.Positive(t, size, "total size should be > 0")
	assert.Len(t, archiveList, 2, "archive list should contain cue and flac files")
	assert.Contains(t, archiveList, cuePath)
	assert.Contains(t, archiveList, flacPath)

	expectedNames := []string{
		"01 - First Song.flac",
		"02 - Second Song.flac",
		"03 - Third Song.flac",
	}
	expectedTitles := []string{"First Song", "Second Song", "Third Song"}

	for idx, expectedName := range expectedNames {
		assert.Equal(t, filepath.Join(outputDir, expectedName), files[idx])
		trackFile, err := os.Open(files[idx])
		require.NoError(t, err, "opening track FLAC file: %s", files[idx])
		trackStream, err := flac.Parse(trackFile)
		require.NoError(t, err, "parsing track FLAC file: %s", files[idx])
		assert.Equal(t, uint32(testSampleRate), trackStream.Info.SampleRate)
		assert.Equal(t, uint8(testNChannels), trackStream.Info.NChannels)
		assert.Positive(t, trackStream.Info.NSamples, "track should have samples")
		// Split tracks should include VorbisComment with ALBUM, ARTIST, TITLE, TRACKNUMBER for Lidarr/import.
		var vorbis *meta.VorbisComment

		for _, blk := range trackStream.Blocks {
			if vc, ok := blk.Body.(*meta.VorbisComment); ok {
				vorbis = vc
				break
			}
		}

		require.NotNil(t, vorbis, "track %s should have VorbisComment metadata", files[idx])

		tagMap := make(map[string]string)
		for _, pair := range vorbis.Tags {
			tagMap[pair[0]] = pair[1]
		}

		assert.Equal(t, "Test Album", tagMap["ALBUM"], "ALBUM tag from CUE TITLE")
		assert.Equal(t, "Test Artist", tagMap["ARTIST"], "ARTIST tag from CUE PERFORMER")
		assert.Equal(t, expectedTitles[idx], tagMap["TITLE"], "TITLE tag from track")
		assert.Equal(t, strconv.Itoa(idx+1), tagMap["TRACKNUMBER"], "TRACKNUMBER")
		require.NoError(t, trackFile.Close())
	}
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
	assert.Len(t, files, 3, "expected 2 track files + CUE sheet")
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

// TestCueWavReferenceFlacFile verifies that when the CUE says FILE "album.wav" WAVE
// but only album.flac exists on disk, we use the .flac file (common mislabeling).
func TestCueWavReferenceFlacFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)
	// CUE references .wav but we only have .flac
	cueContent := strings.Join([]string{
		`PERFORMER "Artist"`,
		`TITLE "Album"`,
		`FILE "album.wav" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track One"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Track Two"`,
		`    INDEX 01 00:30:00`,
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
	require.NoError(t, err, "ExtractCUE with CUE referencing .wav but .flac on disk")
	assert.Len(t, files, 3, "expected 2 track files + CUE sheet")
	assert.Len(t, archiveList, 2)
	assert.Positive(t, size)
	// Archive list should include the actual flac path we used, not the .wav path
	assert.Contains(t, archiveList, flacPath)
}

// TestCueFallbackSameBasename verifies that when the FILE line does not match any file
// (e.g. O vs Ö or encoding mismatch), we use the FLAC with the same base name as the CUE file.
func TestCueFallbackSameBasename(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(60 * testSampleRate)
	// FLAC on disk has same base name as CUE, but CUE FILE line references a different name.
	flacPath := filepath.Join(tmpDir, "Album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	cueContent := strings.Join([]string{
		`PERFORMER "Artist"`,
		`TITLE "Album"`,
		`FILE "Other Name.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "Track One"`,
		`    INDEX 01 00:00:00`,
		`  TRACK 02 AUDIO`,
		`    TITLE "Track Two"`,
		`    INDEX 01 00:30:00`,
	}, "\n") + "\n"
	cuePath := filepath.Join(tmpDir, "Album.cue")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	size, files, archiveList, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "ExtractCUE should find Album.flac via same-basename fallback")
	assert.Len(t, files, 3)
	assert.Len(t, archiveList, 2)
	assert.Positive(t, size)
	assert.Contains(t, archiveList, flacPath)
}

// TestCueUTF16LE verifies that a CUE file encoded as UTF-16 LE with BOM is parsed correctly.
func TestCueUTF16LE(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)
	//nolint:lll // it's ok.
	cueContent := "PERFORMER \"Artist\"\r\nTITLE \"Album\"\r\nFILE \"album.flac\" WAVE\r\n  TRACK 01 AUDIO\r\n    TITLE \"Track One\"\r\n    INDEX 01 00:00:00\r\n"
	// Encode as UTF-16 LE with BOM (common for CUE sheets from Windows).
	u16 := utf16.Encode([]rune(cueContent))
	buf := make([]byte, 0, 2+len(u16)*2)

	buf = append(buf, 0xFF, 0xFE)
	for _, v := range u16 {
		buf = append(buf, byte(v), byte(v>>8))
	}

	cuePath := filepath.Join(tmpDir, "album.cue")
	require.NoError(t, os.WriteFile(cuePath, buf, 0o600))

	xFile := &xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	}

	size, files, _, err := xtractr.ExtractCUE(xFile)
	require.NoError(t, err, "UTF-16 LE CUE should parse and extract")
	assert.Positive(t, size)
	assert.Len(t, files, 2, "expected 1 track + CUE sheet")
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
	assert.Len(t, files, 3, "expected 2 track files + CUE sheet")

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
	assert.Len(t, files, 3, "expected 2 track files + CUE sheet")

	assert.Equal(t, "01 - Song With - Slash.flac", filepath.Base(files[0]))
	assert.Equal(t, "02 - Song- With Special Chars.flac", filepath.Base(files[1]))
}

// TestCueSmartQuoteInTitle verifies that track titles with Unicode smart quote (U+2019)
// are sanitized to ASCII apostrophe in output filenames so tools like Lidarr can find files.
func TestCueSmartQuoteInTitle(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(60 * testSampleRate)
	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, totalSamples)

	// U+2019 is RIGHT SINGLE QUOTATION MARK (curly apostrophe), often from CUE sheets.
	cueContent := strings.Join([]string{
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "It's Hard to Find a Way"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	// Replace straight apostrophe with U+2019 in the CUE content.
	cueContent = strings.ReplaceAll(cueContent, "It's", "It\u2019s")
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
	// Output filename must use ASCII apostrophe so Lidarr can find the file.
	assert.Equal(t, "01 - It's Hard to Find a Way.flac", filepath.Base(files[0]))
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
	assert.Len(t, files, 2, "expected 1 track file + CUE sheet")
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
