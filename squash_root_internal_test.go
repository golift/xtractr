package xtractr

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSquashRootCountsEmptyTopDir(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	video := filepath.Join(out, "VIDEO_TS")
	audio := filepath.Join(out, "AUDIO_TS")

	require.NoError(t, os.Mkdir(video, 0o700))

	inner := filepath.Join(video, "FILE.TXT")
	require.NoError(t, os.WriteFile(inner, []byte("x"), 0o600))

	xFile := &XFile{
		OutputDir:  out,
		SquashRoot: true,
		DirMode:    0o755,
		log:        NoLogger(),
	}
	xFile.bindMoveFiles()

	require.NoError(t, xFile.mkDir(audio, time.Time{}))
	xFile.noteWritten(inner)

	_, err := xFile.squashRoot([]string{inner})
	require.NoError(t, err)
	require.FileExists(t, inner)
	require.NoFileExists(t, filepath.Join(out, "FILE.TXT"))
}
