package xtractr

import (
	"os"
	"path/filepath"
	"testing"

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
