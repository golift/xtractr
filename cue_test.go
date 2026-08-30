package xtractr_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestCueParseCueSheet(t *testing.T) {
	t.Parallel()

	assert.True(t, xtractr.IsArchiveFile("test.cue"), ".cue should be recognized as an archive file")
	assert.True(t, xtractr.IsArchiveFile("TEST.CUE"), ".CUE (uppercase) should be recognized")
	assert.True(t, xtractr.IsArchiveFile("album.cue.txt"), ".cue.txt should be recognized as an archive file")
	assert.True(t, xtractr.IsArchiveFile("ALBUM.CUE.TXT"), ".CUE.TXT (uppercase) should be recognized")
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

func TestCueExtractCUE_ReplacesTrackSymlink(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmpDir, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	outputDir := filepath.Join(tmpDir, "output")
	require.NoError(t, os.MkdirAll(outputDir, 0o750))

	victim := filepath.Join(tmpDir, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	trackPath := filepath.Join(outputDir, "01 - First Song.flac")
	require.NoError(t, os.Symlink(victim, trackPath))

	flacPath := filepath.Join(tmpDir, "album.flac")
	generateTestFLAC(t, flacPath, uint64(testBlockSize)*2)

	cuePath := filepath.Join(tmpDir, "album.cue")
	cueContent := strings.Join([]string{
		`FILE "album.flac" WAVE`,
		`  TRACK 01 AUDIO`,
		`    TITLE "First Song"`,
		`    INDEX 01 00:00:00`,
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	_, files, _, err := xtractr.ExtractCUE(&xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	require.Contains(t, files, trackPath)

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "must not follow planted track symlink")

	info, err := os.Lstat(trackPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
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

// TestCueTxtExtractViaExtractFile covers release groups that ship the sheet as .cue.txt.
func TestCueTxtExtractViaExtractFile(t *testing.T) {
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
	cuePath := filepath.Join(tmpDir, "album.cue.txt")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	found := xtractr.FindCompressedFiles(xtractr.Filter{Path: tmpDir})
	require.Contains(t, found.List(), cuePath)

	size, files, archiveList, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	})
	require.NoError(t, err, "ExtractFile with .cue.txt")
	assert.Len(t, files, 3, "expected 2 track files + CUE sheet")
	assert.Len(t, archiveList, 2)
	assert.Positive(t, size)
	assert.Contains(t, files, filepath.Join(outputDir, "album.cue.txt"))
}

// TestCueTxtBasenameFallback ensures album.cue.txt still finds album.flac when
// the FILE line does not match (filepath.Ext would otherwise leave a ".cue" stem).
func TestCueTxtBasenameFallback(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	outputDir := filepath.Join(tmpDir, "output")

	totalSamples := uint64(60 * testSampleRate)
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
	cuePath := filepath.Join(tmpDir, "Album.cue.txt")
	require.NoError(t, os.WriteFile(cuePath, []byte(cueContent), 0o600))

	size, files, archiveList, err := xtractr.ExtractCUE(&xtractr.XFile{
		FilePath:  cuePath,
		OutputDir: outputDir,
		FileMode:  0o600,
		DirMode:   0o755,
	})
	require.NoError(t, err, "ExtractCUE should find Album.flac via same-basename fallback")
	assert.Len(t, files, 3)
	assert.Len(t, archiveList, 2)
	assert.Positive(t, size)
	assert.Contains(t, archiveList, flacPath)
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

	foundTxt := false

	for _, ext := range extensions {
		if strings.EqualFold(ext, ".cue.txt") {
			foundTxt = true
			break
		}
	}

	assert.True(t, foundTxt, ".cue.txt should be in supported extensions list")
}
