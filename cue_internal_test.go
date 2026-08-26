package xtractr

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveCueAudioPathTraversal ensures a CUE sheet cannot reference an
// audio file outside the folder the CUE sheet lives in.
func TestResolveCueAudioPathTraversal(t *testing.T) {
	t.Parallel()

	cueDir := t.TempDir()
	outside := filepath.Join(cueDir, "..", "outside.flac")
	require.NoError(t, os.WriteFile(outside, []byte("not audio"), 0o600))

	for _, cueFile := range []string{
		"../outside.flac",
		"../../outside.flac",
		"sub/../../outside.flac",
		"..",
	} {
		_, err := resolveCueAudioPath(cueDir, cueFile, filepath.Join(cueDir, "disc.cue"))
		require.Error(t, err, cueFile)
		assert.ErrorIs(t, err, ErrInvalidPath, cueFile)
	}
}

// TestResolveCueAudioPathNested ensures audio in a subfolder of the CUE sheet
// still resolves (a legitimate layout some rips use).
func TestResolveCueAudioPathNested(t *testing.T) {
	t.Parallel()

	cueDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(cueDir, "disc1"), 0o750))

	audioPath := filepath.Join(cueDir, "disc1", "album.flac")
	require.NoError(t, os.WriteFile(audioPath, []byte("not audio"), 0o600))

	resolved, err := resolveCueAudioPath(cueDir, "disc1/album.flac", filepath.Join(cueDir, "disc.cue"))
	require.NoError(t, err)
	assert.Equal(t, audioPath, resolved)
}

// TestCopyCueToOutputReplacesSymlink is the Copilot finding: os.WriteFile follows
// a pre-existing symlink at destPath, so a planted "disc.cue" -> /victim link
// would overwrite a file outside the output folder.
func TestCopyCueToOutputReplacesSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(tmp, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	out := filepath.Join(tmp, "out")
	require.NoError(t, os.MkdirAll(out, 0o750))

	dest := filepath.Join(out, "disc.cue")
	require.NoError(t, os.Symlink(victim, dest))

	src := filepath.Join(tmp, "src.cue")
	require.NoError(t, os.WriteFile(src, []byte("REM GENRE Test\n"), 0o600))

	require.NoError(t, copyCueToOutput(&XFile{}, src, dest, 0o600))

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "must not follow destPath symlink")

	copied, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("REM GENRE Test\n"), copied)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
}

// TestStreamTracksFLACTinyBoundaryClips splits a fixed-blocksize FLAC at sample
// positions that land 8 samples into and 8 samples before the end of a source
// frame. Those boundary clips are smaller than the minimum block size the FLAC
// format allows for a non-final frame (16 samples), so they must fuse with the
// neighboring frame; written as-is, the encoder records a STREAMINFO minimum
// block size of 8 and strict parsers (including go-flac itself) reject the track.
func TestStreamTracksFLACTinyBoundaryClips(t *testing.T) {
	t.Parallel()

	const (
		blockSize    = 4096
		totalSamples = 5 * blockSize
	)

	tmpDir := t.TempDir()
	flacPath := filepath.Join(tmpDir, "album.flac")
	info := &meta.StreamInfo{
		BlockSizeMin:  blockSize,
		BlockSizeMax:  blockSize,
		SampleRate:    44100,
		NChannels:     2,
		BitsPerSample: 16,
		NSamples:      totalSamples,
	}

	outFile, err := os.Create(flacPath)
	require.NoError(t, err)

	enc, err := flac.NewEncoder(outFile, info)
	require.NoError(t, err)

	for written := 0; written < totalSamples; written += blockSize {
		subframes := make([]*frame.Subframe, 2)
		for channel := range subframes {
			samples := make([]int32, blockSize)
			for i := range samples {
				samples[i] = int32(written + i)
			}

			subframes[channel] = &frame.Subframe{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
				Samples:   samples,
				NSamples:  blockSize,
			}
		}

		require.NoError(t, enc.WriteFrame(&frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true,
				BlockSize:         blockSize,
				SampleRate:        44100,
				Channels:          frame.ChannelsLR,
				BitsPerSample:     16,
			},
			Subframes: subframes,
		}))
	}

	require.NoError(t, enc.Close())

	// Track 1 ends 8 samples into a source frame; track 3 starts 8 samples
	// before a source frame ends. Both edges produce an 8-sample clip.
	trackStarts := []uint64{0, blockSize + 8, 3*blockSize - 8}
	trackEnds := []uint64{blockSize + 8, 3*blockSize - 8, totalSamples}
	cue := &CueSheet{Tracks: []CueTrack{{Number: 1}, {Number: 2}, {Number: 3}}}
	xFile := &XFile{OutputDir: tmpDir, FileMode: 0o600}

	_, files, err := streamTracksFLAC(xFile, flacPath, cue, trackStarts, trackEnds, info, &flacMetadata{Info: info})
	require.NoError(t, err)
	require.Len(t, files, 3)

	expected := []uint64{blockSize + 8, 2*blockSize - 16, 2*blockSize + 8}

	for idx, path := range files {
		trackFile, err := os.Open(path)
		require.NoError(t, err)

		stream, err := flac.New(trackFile)
		require.NoError(t, err, "track %d STREAMINFO must parse", idx+1)
		assert.Equal(t, expected[idx], stream.Info.NSamples, "track %d sample count", idx+1)

		// The source sample value equals its absolute position, so each track must
		// decode to a contiguous run starting at its track start sample. This catches
		// a fusion/redistribution that drops, duplicates, or reorders samples.
		position := trackStarts[idx]

		for frameIdx := 0; ; frameIdx++ {
			frm, err := stream.ParseNext()
			if errors.Is(err, io.EOF) {
				break
			}

			require.NoError(t, err)
			assert.False(t, frm.HasFixedBlockSize, "track %d frame %d", idx+1, frameIdx)
			assert.GreaterOrEqual(t, frm.Subframes[0].NSamples, minFLACBlockSize,
				"track %d frame %d below minimum block size", idx+1, frameIdx)

			for _, sample := range frm.Subframes[0].Samples {
				require.Equal(t, int32(position), sample,
					"track %d frame %d sample out of sequence", idx+1, frameIdx)
				position++
			}
		}

		assert.Equal(t, trackEnds[idx], position, "track %d decoded sample count", idx+1)
		require.NoError(t, trackFile.Close())
	}
}

// TestBalanceFrames covers the frame-pair sizing rules: both-valid pairs pass
// through, small pairs concatenate, and a tiny clip next to a maximum-size frame
// redistributes samples so neither output frame is below the minimum block size.
func TestBalanceFrames(t *testing.T) {
	t.Parallel()

	mkFrame := func(samples int) *frame.Frame {
		subs := make([]*frame.Subframe, 2)
		for channel := range subs {
			subs[channel] = &frame.Subframe{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
				Samples:   make([]int32, samples),
				NSamples:  samples,
			}
		}

		header := frame.Header{
			BlockSize:     uint16(samples),
			SampleRate:    44100,
			Channels:      frame.ChannelsLR,
			BitsPerSample: 16,
		}

		return &frame.Frame{Header: header, Subframes: subs}
	}

	// Both frames already valid: write held, hold next, unchanged.
	write, hold := balanceFrames(mkFrame(4096), mkFrame(4096))
	require.NotNil(t, write)
	require.NotNil(t, hold)
	assert.Equal(t, 4096, write.Subframes[0].NSamples)
	assert.Equal(t, 4096, hold.Subframes[0].NSamples)

	// Small pair: concatenate into the held frame, nothing to write yet.
	write, hold = balanceFrames(mkFrame(8), mkFrame(4096))
	assert.Nil(t, write)
	require.NotNil(t, hold)
	assert.Equal(t, 8+4096, hold.Subframes[0].NSamples)

	// Tiny clip before a maximum-size frame: too large to concatenate, so move
	// samples from the tiny clip onto the next frame; both meet the minimum.
	write, hold = balanceFrames(mkFrame(8), mkFrame(maxFLACBlockSize))
	require.NotNil(t, write)
	require.NotNil(t, hold)
	assert.Equal(t, minFLACBlockSize, write.Subframes[0].NSamples)
	assert.Equal(t, maxFLACBlockSize-8, hold.Subframes[0].NSamples)
	assert.Equal(t, 8+maxFLACBlockSize, write.Subframes[0].NSamples+hold.Subframes[0].NSamples,
		"total samples must be preserved")

	// Tiny clip after a maximum-size frame: move the tail of the large frame
	// forward so both meet the minimum.
	write, hold = balanceFrames(mkFrame(maxFLACBlockSize), mkFrame(8))
	require.NotNil(t, write)
	require.NotNil(t, hold)
	assert.Equal(t, maxFLACBlockSize-8, write.Subframes[0].NSamples)
	assert.Equal(t, minFLACBlockSize, hold.Subframes[0].NSamples)
	assert.Equal(t, maxFLACBlockSize+8, write.Subframes[0].NSamples+hold.Subframes[0].NSamples,
		"total samples must be preserved")
}
