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
