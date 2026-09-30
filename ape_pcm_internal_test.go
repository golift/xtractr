package xtractr

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/ape"
)

func TestSplitAPEPlayableEncodesAPE(t *testing.T) {
	t.Parallel()

	const (
		rate     = 8000
		channels = 2
		bits     = 16
		seconds  = 2
		level    = 2000
	)

	pcm := bytes.Repeat([]byte{0x10, 0x20, 0x30, 0x40}, rate*seconds)
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "album.ape")
	out, err := os.Create(audioPath)
	require.NoError(t, err)

	stream := ape.Stream{SampleRate: rate, Channels: channels, Bits: bits}
	err = ape.Encode(out, pcm, stream, &ape.Options{BlocksPerFrame: 32})
	require.NoError(t, err)
	require.NoError(t, out.Close())

	cuePath := filepath.Join(dir, "album.cue")
	cueText := "PERFORMER \"Band\"\nTITLE \"Album\"\nFILE \"album.ape\" WAVE\n" +
		"  TRACK 01 AUDIO\n" +
		"    TITLE \"First Song\"\n" +
		"    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    TITLE \"Second Song\"\n" +
		"    INDEX 01 00:01:00\n"
	require.NoError(t, os.WriteFile(cuePath, []byte(cueText), 0o600))

	outDir := filepath.Join(dir, "output")
	xFile := &XFile{
		FilePath:  cuePath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o755,
		APEOpts: APEOpts{
			Compression: level,
		},
	}

	_, files, _, err := ExtractCUE(xFile)
	require.NoError(t, err)

	align := channels * bits / bitsPerByte
	want := []struct {
		name string
		pcm  []byte
	}{
		{"01 - First Song.ape", pcm[:rate*align]},
		{"02 - Second Song.ape", pcm[rate*align:]},
	}

	for _, track := range want {
		path := filepath.Join(outDir, track.name)
		assert.Contains(t, files, path)

		info, parseErr := parseAPE(path)
		require.NoError(t, parseErr)
		assert.Equal(t, uint16(level), info.Header.CompressionLevel)

		got, _, decErr := ape.DecodeFile(path)
		require.NoError(t, decErr)
		assert.Equal(t, track.pcm, got)
	}
}

func TestSplitAPEPlayableWAVAndFLAC(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(2)

	for _, format := range []AudioFormat{AudioFormatWAV, "FLAC"} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			outDir := splitTestAPE(t, pcm, stream, APEOpts{Output: format, Compression: 5000})
			ext := ".wav"
			read := wavData

			if format != AudioFormatWAV {
				ext = ".flac"
				read = flacPCM16
			}

			want := []struct {
				name string
				pcm  []byte
			}{
				{"01 - First Song" + ext, pcm[:stream.SampleRate*4]},
				{"02 - Second Song" + ext, pcm[stream.SampleRate*4:]},
			}

			for _, track := range want {
				path := filepath.Join(outDir, track.name)
				got, err := read(path)
				require.NoError(t, err)
				assert.Equal(t, track.pcm, got)
			}

			if ext == ".flac" {
				flacHasTag(t, filepath.Join(outDir, "01 - First Song.flac"), "TITLE", "First Song")
				flacHasTag(t, filepath.Join(outDir, "01 - First Song.flac"), "ARTIST", "Band")
			} else {
				raw, err := os.ReadFile(filepath.Join(outDir, "01 - First Song.wav"))
				require.NoError(t, err)
				assert.True(t, bytes.Contains(raw, []byte("INAM")))
				assert.True(t, bytes.Contains(raw, []byte("First Song")))
				assert.True(t, bytes.Contains(raw, []byte("IART")))
				assert.True(t, bytes.Contains(raw, []byte("Band")))
			}
		})
	}
}

func TestSplitAPEPlayableRejectsFormat(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(1)
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "album.ape")
	writeTestAPE(t, audioPath, pcm, stream)
	writeTestCUE(t, filepath.Join(dir, "album.cue"))

	_, _, _, err := ExtractCUE(&XFile{ //nolint:dogsled // only the error matters here.
		FilePath:  filepath.Join(dir, "album.cue"),
		OutputDir: filepath.Join(dir, "output"),
		FileMode:  0o600,
		DirMode:   0o755,
		APEOpts:   APEOpts{Output: "aiff"},
	})
	require.ErrorIs(t, err, ErrUnsupportedAPEOutput)
}

func TestSplitAPEPlayableWAVNeedsDecode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "album.ape"), []byte("not ape"), 0o600))
	writeTestCUE(t, filepath.Join(dir, "album.cue"))

	outDir := filepath.Join(dir, "output")
	_, _, _, err := ExtractCUE(&XFile{ //nolint:dogsled // only the error matters here.
		FilePath:  filepath.Join(dir, "album.cue"),
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o755,
		APEOpts:   APEOpts{Output: AudioFormatWAV},
	})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(outDir, "01 - First Song.wav"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestConvertAPE(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(1)

	for _, format := range []AudioFormat{"", AudioFormatWAV, AudioFormatFLAC} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			src := filepath.Join(dir, "song.ape")
			writeTestAPE(t, src, pcm, stream)

			outDir := filepath.Join(dir, "out")
			size, files, err := ConvertAPE(&XFile{
				FilePath:  src,
				OutputDir: outDir,
				FileMode:  0o600,
				DirMode:   0o755,
				APEOpts:   APEOpts{Output: format, Compression: 3000},
			})
			require.NoError(t, err)
			require.Len(t, files, 1)
			assert.Positive(t, size)

			switch format {
			case "", AudioFormatAPE:
				assert.Equal(t, filepath.Join(outDir, "song.ape"), files[0])
				got, _, decErr := ape.DecodeFile(files[0])
				require.NoError(t, decErr)
				assert.Equal(t, pcm, got)
			case AudioFormatWAV:
				got, readErr := wavData(files[0])
				require.NoError(t, readErr)
				assert.Equal(t, pcm, got)
			case AudioFormatFLAC:
				got, readErr := flacPCM16(files[0])
				require.NoError(t, readErr)
				assert.Equal(t, pcm, got)
			}
		})
	}
}

func TestConvertAPERejectsFLACWidth(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "wide.ape")
	stream := ape.Stream{SampleRate: 8000, Channels: 1, Bits: 32}
	writeTestAPE(t, src, bytes.Repeat([]byte{1, 2, 3, 4}, 32), stream)

	_, _, err := ConvertAPE(&XFile{
		FilePath:  src,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  0o600,
		DirMode:   0o755,
		APEOpts:   APEOpts{Output: AudioFormatFLAC},
	})
	require.ErrorIs(t, err, ErrUnsupportedAPEOutput)
}

func TestConvertAPEDefaultCompression(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(1)
	dir := t.TempDir()
	src := filepath.Join(dir, "song.ape")
	writeTestAPE(t, src, pcm, stream)

	_, files, err := ConvertAPE(&XFile{
		FilePath:  src,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  0o600,
		DirMode:   0o755,
	})
	require.NoError(t, err)

	info, parseErr := parseAPE(files[0])
	require.NoError(t, parseErr)
	assert.Equal(t, uint16(ape.CompressionNormal), info.Header.CompressionLevel)
}

func TestConvertAPERefusesOverwrite(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(1)
	dir := t.TempDir()
	src := filepath.Join(dir, "song.ape")
	writeTestAPE(t, src, pcm, stream)

	_, _, err := ConvertAPE(&XFile{
		FilePath:  src,
		OutputDir: dir,
		FileMode:  0o600,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, ErrAPEOverwrite)

	got, _, decErr := ape.DecodeFile(src)
	require.NoError(t, decErr)
	assert.Equal(t, pcm, got)
}

func TestSplitAPEPlayableHonorsMaxFiles(t *testing.T) {
	t.Parallel()

	pcm, stream := stereoSeconds(2)
	dir := t.TempDir()
	writeTestAPE(t, filepath.Join(dir, "album.ape"), pcm, stream)
	writeTestCUE(t, filepath.Join(dir, "album.cue"))

	_, _, _, err := ExtractCUE(&XFile{ //nolint:dogsled // only the error matters here.
		FilePath:  filepath.Join(dir, "album.cue"),
		OutputDir: filepath.Join(dir, "output"),
		FileMode:  0o600,
		DirMode:   0o755,
		MaxFiles:  1,
		APEOpts:   APEOpts{Output: AudioFormatWAV},
	})
	require.ErrorIs(t, err, ErrMaxFiles)
}

func TestWriteWAVPadsOddData(t *testing.T) {
	t.Parallel()

	pcm := bytes.Repeat([]byte{0x80}, 17)
	stream := ape.Stream{SampleRate: 8000, Channels: 1, Bits: 8}
	path := filepath.Join(t.TempDir(), "odd.wav")
	out, err := os.Create(path)
	require.NoError(t, err)

	require.NoError(t, writeWAV(out, pcm, stream, nil))
	require.NoError(t, out.Close())

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, uint32(len(raw)-8), binary.LittleEndian.Uint32(raw[4:8]))
	assert.Equal(t, byte(0), raw[len(raw)-1])

	got, err := wavData(path)
	require.NoError(t, err)
	assert.Equal(t, pcm, got)
}

func TestAPETrackTagsUseAlbumPerformer(t *testing.T) {
	t.Parallel()

	tags := apeTrackTags(
		&CueSheet{Performer: "Band", Title: "Album"},
		&CueTrack{Number: 1, Title: "Song"},
	)
	assert.Equal(t, "Band", tags["Artist"])
	assert.Equal(t, "Album", tags["Album"])
}

func TestSplitAPEFrameCopyDecodes(t *testing.T) {
	t.Parallel()

	const blocks = 1000

	pcm, stream := stereoSeconds(2)
	dir := t.TempDir()
	src := filepath.Join(dir, "album.ape")
	out, err := os.Create(src)
	require.NoError(t, err)

	err = ape.Encode(out, pcm, stream, &ape.Options{Compression: ape.CompressionFast, BlocksPerFrame: blocks})
	require.NoError(t, err)
	require.NoError(t, out.Close())

	// 8000 Hz and 5 CD frames is sample 8533, inside frame 8 (frames are 1000 samples).
	xFile := &XFile{OutputDir: filepath.Join(dir, "out"), FileMode: 0o600, DirMode: 0o755}
	cue := &CueSheet{Tracks: []CueTrack{{Number: 1, Title: "First"}, {Number: 2, Title: "Second"}}}
	_, files, err := splitAPE(xFile, src, cue, []cueTimestamp{{}, {seconds: 1, frames: 5}})
	require.NoError(t, err)

	track1, err := parseAPE(files[0])
	require.NoError(t, err)
	assert.Equal(t, uint32(blocks), track1.Header.FinalFrameBlocks)

	got, _, err := ape.DecodeFile(files[0])
	require.NoError(t, err)
	assert.Len(t, got, 9*blocks*4)

	_, _, err = ape.DecodeFile(files[1])
	require.NoError(t, err)
}

func stereoSeconds(seconds int) ([]byte, ape.Stream) {
	const (
		rate     = 8000
		channels = 2
		bits     = 16
	)

	stream := ape.Stream{SampleRate: rate, Channels: channels, Bits: bits}

	return bytes.Repeat([]byte{0x10, 0x20, 0x30, 0x40}, rate*seconds), stream
}

func writeTestAPE(t *testing.T, path string, pcm []byte, stream ape.Stream) {
	t.Helper()

	out, err := os.Create(path)
	require.NoError(t, err)

	err = ape.Encode(out, pcm, stream, &ape.Options{BlocksPerFrame: 32})
	require.NoError(t, err)
	require.NoError(t, out.Close())
}

func writeTestCUE(t *testing.T, path string) {
	t.Helper()

	cueText := "PERFORMER \"Band\"\nTITLE \"Album\"\nFILE \"album.ape\" WAVE\n" +
		"  TRACK 01 AUDIO\n" +
		"    TITLE \"First Song\"\n" +
		"    INDEX 01 00:00:00\n" +
		"  TRACK 02 AUDIO\n" +
		"    TITLE \"Second Song\"\n" +
		"    INDEX 01 00:01:00\n"
	require.NoError(t, os.WriteFile(path, []byte(cueText), 0o600))
}

func splitTestAPE(t *testing.T, pcm []byte, stream ape.Stream, opt APEOpts) string {
	t.Helper()

	dir := t.TempDir()
	writeTestAPE(t, filepath.Join(dir, "album.ape"), pcm, stream)
	writeTestCUE(t, filepath.Join(dir, "album.cue"))

	outDir := filepath.Join(dir, "output")
	_, files, _, err := ExtractCUE(&XFile{
		FilePath:  filepath.Join(dir, "album.cue"),
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o755,
		APEOpts:   opt,
	})
	require.NoError(t, err)
	require.NotEmpty(t, files)

	return outDir
}

func wavData(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading wav: %w", err)
	}

	if len(raw) < wavFmtLen || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, errors.New("not a wav file")
	}

	off := 12
	for off+8 <= len(raw) {
		chunkID := string(raw[off : off+4])
		size := int(binary.LittleEndian.Uint32(raw[off+4 : off+8]))
		off += 8

		if size < 0 || off+size > len(raw) {
			return nil, errors.New("truncated wav chunk")
		}

		if chunkID == "data" {
			return raw[off : off+size], nil
		}

		off += size
		if size%2 != 0 {
			off++
		}
	}

	return nil, errors.New("wav data chunk missing")
}

func flacPCM16(path string) ([]byte, error) {
	file, stream, err := openFLAC(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var out []byte

	for {
		parsed, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, fmt.Errorf("reading flac frame: %w", err)
		}

		for sample := range parsed.Subframes[0].NSamples {
			for _, sub := range parsed.Subframes {
				var buf [2]byte
				binary.LittleEndian.PutUint16(buf[:], uint16(int16(sub.Samples[sample])))
				out = append(out, buf[:]...)
			}
		}
	}
}

func openFLAC(path string) (*os.File, *flac.Stream, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading flac: %w", err)
	}

	stream, err := flac.Parse(file)
	if err != nil {
		_ = file.Close()

		return nil, nil, fmt.Errorf("parsing flac: %w", err)
	}

	return file, stream, nil
}

func flacHasTag(t *testing.T, path, key, value string) {
	t.Helper()

	file, stream, err := openFLAC(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	for _, block := range stream.Blocks {
		comment, ok := block.Body.(*meta.VorbisComment)
		if !ok {
			continue
		}

		for _, tag := range comment.Tags {
			if tag[0] == key && tag[1] == value {
				return
			}
		}
	}

	t.Fatalf("flac %s missing %s %q", path, key, value)
}
