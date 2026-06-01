package xtractr_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestExtractRAR(t *testing.T) {
	t.Parallel()

	size, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  "./test_data/archive.rar",
		OutputDir: t.TempDir(),
		Password:  "testing", // one of these is right. :)
		Passwords: []string{"testingmore", "some_password", "some_other"},
	})

	require.NoError(t, err)
	assert.Equal(t, testDataSize, size)
	assert.Len(t, archives, 1)
	assert.Len(t, files, len(filesInTestArchive))
}

// TestExtractRARMultiVolume guards against the regression where only the entry
// file (instead of every volume) was returned in the archive list, which left
// the sibling parts of a multi-part archive orphaned during cleanup.
func TestExtractRARMultiVolume(t *testing.T) {
	t.Parallel()

	const volumeCount = 4

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join("test_data", "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, volumeCount, "every volume must be returned for cleanup")

	for idx := 1; idx <= volumeCount; idx++ {
		want := filepath.Join("test_data", fmt.Sprintf("multivol.part%d.rar", idx))
		assert.Contains(t, archives, want, "volume %d must be present in the archive list", idx)
	}
}

// TestExtractRARMultiVolumeAbsolutePath verifies that when the caller passes an
// absolute path to the entry archive, every returned volume is an absolute path
// resolved beside the entry file. rardecode reports bare volume basenames, so
// this exercises normalizeVolumes joining them onto the entry archive's
// directory rather than the process working directory.
func TestExtractRARMultiVolumeAbsolutePath(t *testing.T) {
	t.Parallel()

	const volumeCount = 4

	dir, err := filepath.Abs("test_data")
	require.NoError(t, err)

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join(dir, "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, volumeCount, "every volume must be returned for cleanup")

	for idx := 1; idx <= volumeCount; idx++ {
		want := filepath.Join(dir, fmt.Sprintf("multivol.part%d.rar", idx))
		assert.Contains(t, archives, want, "volume %d must be returned as an absolute path", idx)
	}
}
