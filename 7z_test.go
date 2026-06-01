package xtractr_test

import (
	"fmt"
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
