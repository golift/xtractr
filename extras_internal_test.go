package xtractr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPick(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, pick(0, 0))
	assert.Equal(t, 8, pick(8, 0))
	assert.Equal(t, 4, pick(0, 4))
	assert.Equal(t, 8, pick(8, 4))
	assert.Equal(t, -1, pick(-1, 0))
	assert.Equal(t, -1, pick(0, -1))
	assert.Equal(t, -1, pick(-1, 4))
	assert.Equal(t, uint64(3), pick(uint64(0), uint64(3)))
}

func TestExtrasFilterAcceptSkipsRecursionPaths(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keep := filepath.Join(dir, "keep.zip")
	skip := filepath.Join(dir, "copied-cue.zip")
	extra := filepath.Join(dir, "extra.zip")

	for _, path := range []string{skip, keep, extra} {
		require.NoError(t, os.WriteFile(path, []byte("pk"), 0o600))
	}

	queue := &Xtractr{config: &Config{}}
	filter := queue.extrasFilter(&Response{
		X:               &Xtract{MaxNested: 1},
		Output:          dir,
		SkipOnRecursion: []string{skip},
	})
	require.Equal(t, 2, filter.MaxArchives)

	found := FindCompressedFiles(filter)
	assert.Equal(t, 2, found.Count(), "skipped CUE copy must not consume MaxArchives")
	assert.NotContains(t, found.List(), skip)
	assert.Contains(t, found.List(), keep)
	assert.Contains(t, found.List(), extra)
}
