package xtractr_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

// TestExtract7zMultiVolume guards against the regression where only the entry
// file (instead of every volume) was returned in the archive list, which left
// the sibling parts of a multi-part archive orphaned during cleanup.
// It runs both the sequential and the parallel (FileWorkers > 1) code paths,
// since each path returns the archive list independently.
func TestExtract7zMultiVolume(t *testing.T) {
	t.Parallel()

	const volumeCount = 2

	for _, workers := range []int{0, 4} {
		t.Run(fmt.Sprintf("FileWorkers=%d", workers), func(t *testing.T) {
			t.Parallel()

			_, files, archives, err := xtractr.Extract7z(&xtractr.XFile{
				FilePath:    filepath.Join("test_data", "multivol.7z.001"),
				OutputDir:   t.TempDir(),
				FileMode:    xtractr.DefaultFileMode,
				DirMode:     xtractr.DefaultDirMode,
				FileWorkers: workers,
			})

			require.NoError(t, err)
			assert.NotEmpty(t, files, "files should have been extracted")
			assert.Len(t, archives, volumeCount, "every volume must be returned for cleanup")

			for idx := 1; idx <= volumeCount; idx++ {
				want := filepath.Join("test_data", fmt.Sprintf("multivol.7z.%03d", idx))
				assert.Contains(t, archives, want, "volume %d must be present in the archive list", idx)
			}
		})
	}
}

// TestExtract7zMissingVolume covers a .7z.001 whose start header records a set
// longer than the volumes on disk. The header walk used to fail inside sevenzip
// with an unexpected EOF after the archive had already been accepted.
func TestExtract7zMissingVolume(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("test_data", "multivol.7z.001"))
	require.NoError(t, err)

	dir := t.TempDir()
	path := filepath.Join(dir, "multivol.7z.001")
	require.NoError(t, os.WriteFile(path, data, 0o600)) //nolint:gosec // fixed name in a temp dir.

	out := t.TempDir()
	size, files, archives, err := xtractr.Extract7z(&xtractr.XFile{
		FilePath:  path,
		OutputDir: out,
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})
	require.ErrorIs(t, err, xtractr.ErrMissingVolume)
	require.ErrorContains(t, err, "multivol.7z.002")
	assert.False(t, xtractr.IsLimitError(err), "a missing volume is not a configured limit: %v", err)
	assert.Zero(t, size)
	assert.Empty(t, files)
	assert.Empty(t, archives)

	written, readErr := os.ReadDir(out)
	require.NoError(t, readErr)
	assert.Empty(t, written)
}

// TestExtract7zHeaderSpansVolumes splits the fixture into 20-byte volumes, so
// the 32-byte start header begins in .001 and continues in .002.
func TestExtract7zHeaderSpansVolumes(t *testing.T) {
	t.Parallel()

	whole := read7zFixture(t)

	const volSize = 20

	t.Run("short set", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := write7zVolumes(t, dir, "span.7z", whole[:volSize*2], volSize)
		out := t.TempDir()
		size, files, archives, err := xtractr.Extract7z(&xtractr.XFile{
			FilePath:  path,
			OutputDir: out,
			FileMode:  xtractr.DefaultFileMode,
			DirMode:   xtractr.DefaultDirMode,
		})
		require.ErrorIs(t, err, xtractr.ErrMissingVolume)
		require.ErrorContains(t, err, "span.7z.003")
		assert.False(t, xtractr.IsLimitError(err), "a missing volume is not a configured limit: %v", err)
		assert.Zero(t, size)
		assert.Empty(t, files)
		assert.Empty(t, archives)

		written, readErr := os.ReadDir(out)
		require.NoError(t, readErr)
		assert.Empty(t, written)
	})

	t.Run("header not on disk", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := write7zVolumes(t, dir, "span.7z", whole[:volSize], volSize)
		_, _, _, err := xtractr.Extract7z(&xtractr.XFile{
			FilePath:  path,
			OutputDir: t.TempDir(),
			FileMode:  xtractr.DefaultFileMode,
			DirMode:   xtractr.DefaultDirMode,
		})
		require.Error(t, err)
		require.NotErrorIs(t, err, xtractr.ErrMissingVolume)
	})

	t.Run("whole set", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := write7zVolumes(t, dir, "span.7z", whole, volSize)
		_, files, archives, err := xtractr.Extract7z(&xtractr.XFile{
			FilePath:  path,
			OutputDir: t.TempDir(),
			FileMode:  xtractr.DefaultFileMode,
			DirMode:   xtractr.DefaultDirMode,
		})
		require.NoError(t, err)
		assert.NotEmpty(t, files)
		assert.NotEmpty(t, archives)
	})
}

func read7zFixture(t *testing.T) []byte {
	t.Helper()

	var whole []byte

	for _, name := range []string{"multivol.7z.001", "multivol.7z.002"} {
		data, err := os.ReadFile(filepath.Join("test_data", name))
		require.NoError(t, err)

		whole = append(whole, data...)
	}

	return whole
}

func write7zVolumes(t *testing.T, dir, stem string, data []byte, volSize int) string {
	t.Helper()

	var first string

	for idx, off := 1, 0; off < len(data); idx++ {
		end := min(off+volSize, len(data))
		part := filepath.Join(dir, fmt.Sprintf("%s.%03d", stem, idx))

		if first == "" {
			first = part
		}

		require.NoError(t, os.WriteFile(part, data[off:end], 0o600))

		off = end
	}

	return first
}
