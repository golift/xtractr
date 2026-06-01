package xtractr

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeVolumes(t *testing.T) {
	t.Parallel()

	t.Run("empty volumes falls back to cleaned entry path", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t,
			[]string{filepath.Join("test_data", "archive.rar")},
			normalizeVolumes(nil, filepath.Join(".", "test_data", "archive.rar")),
		)
	})

	t.Run("bare volume names resolve beside entry path", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t,
			[]string{
				filepath.Join("test_data", "multivol.part1.rar"),
				filepath.Join("test_data", "multivol.part2.rar"),
			},
			normalizeVolumes(
				[]string{"multivol.part1.rar", "multivol.part2.rar"},
				filepath.Join(".", "test_data", "multivol.part1.rar"),
			),
		)
	})

	t.Run("relative paths with directories are preserved", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t,
			[]string{
				filepath.Join("test_data", "multivol.7z.001"),
				filepath.Join("test_data", "multivol.7z.002"),
			},
			normalizeVolumes(
				[]string{
					filepath.Join(".", "test_data", "multivol.7z.001"),
					filepath.Join(".", "test_data", "multivol.7z.002"),
				},
				filepath.Join(".", "test_data", "multivol.7z.001"),
			),
		)
	})

	t.Run("relative paths with directories are preserved regardless of existence", func(t *testing.T) {
		t.Parallel()

		volume := filepath.Join("other", "vol.part2.rar")

		assert.Equal(t,
			[]string{volume},
			normalizeVolumes([]string{volume}, filepath.Join("test_data", "vol.part1.rar")),
		)
	})

	t.Run("absolute paths are preserved", func(t *testing.T) {
		t.Parallel()

		volume := filepath.Join(t.TempDir(), "vol.part2.rar")

		assert.Equal(t,
			[]string{volume},
			normalizeVolumes([]string{volume}, filepath.Join("test_data", "vol.part1.rar")),
		)
	})

	t.Run("reported paths are cleaned", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t,
			[]string{filepath.Join("test_data", "vol.part2.rar")},
			normalizeVolumes(
				[]string{filepath.Join(".", "other", "..", "vol.part2.rar")},
				filepath.Join(".", "test_data", "vol.part1.rar"),
			),
		)
	})
}
