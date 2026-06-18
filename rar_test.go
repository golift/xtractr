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

	parts, err := filepath.Glob(filepath.Join("test_data", "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, parts, "multi-volume rar fixtures must be present")

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join("test_data", "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(parts), "every volume must be returned for cleanup")

	for _, part := range parts {
		assert.Contains(t, archives, part, "volume %s must be present in the archive list", part)
	}
}

// TestExtractRARMultiVolumeAbsolutePath verifies that when the caller passes an
// absolute path to the entry archive, every returned volume is an absolute path
// resolved beside the entry file. rardecode reports bare volume basenames, so
// this exercises normalizeVolumes joining them onto the entry archive's
// directory rather than the process working directory.
func TestExtractRARMultiVolumeAbsolutePath(t *testing.T) {
	t.Parallel()

	dir, err := filepath.Abs("test_data")
	require.NoError(t, err)

	parts, err := filepath.Glob(filepath.Join(dir, "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, parts, "multi-volume rar fixtures must be present")

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join(dir, "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(parts), "every volume must be returned for cleanup")

	for _, part := range parts {
		assert.Contains(t, archives, part, "volume %s must be returned as an absolute path", part)
	}
}

// TestExtractRARMultiVolumeOldScheme covers the legacy RAR volume naming scheme
// (multivol.rar, multivol.r00, multivol.r01, ...) which is exactly the layout
// this PR's cleanup fix must handle. To avoid committing additional binary
// fixtures, the existing multi-part payloads are symlinked under the old names
// inside a temp dir; rardecode follows the chain and every volume must be
// returned for cleanup.
func TestExtractRARMultiVolumeOldScheme(t *testing.T) {
	t.Parallel()

	src, err := filepath.Abs("test_data")
	require.NoError(t, err)

	payloads, err := filepath.Glob(filepath.Join(src, "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, payloads, "multi-volume rar fixtures must be present")

	dir := t.TempDir()
	wantVolumes := make([]string, len(payloads))

	for idx, payload := range payloads {
		name := fmt.Sprintf("multivol.r%02d", idx-1)
		if idx == 0 {
			name = "multivol.rar" // first volume of the old scheme is the .rar file
		}

		link := filepath.Join(dir, name)
		if err := os.Symlink(payload, link); err != nil {
			t.Skipf("symlinks unavailable on this platform: %v", err)
		}

		wantVolumes[idx] = link
	}

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join(dir, "multivol.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(wantVolumes), "every old-scheme volume must be returned for cleanup")

	for _, want := range wantVolumes {
		assert.Contains(t, archives, want, "old-scheme volume %s must be present in the archive list", want)
	}
}
