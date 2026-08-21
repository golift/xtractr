package xtractr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

	t.Run("empty and dot volumes are dropped", func(t *testing.T) {
		t.Parallel()

		entry := filepath.Join("test_data", "vol.part1.rar")

		assert.Equal(t,
			[]string{filepath.Join("test_data", "vol.part2.rar")},
			normalizeVolumes([]string{"", ".", "vol.part2.rar"}, entry),
			"empty/dot entries must never normalize to the entry directory",
		)
	})

	t.Run("only empty volumes fall back to entry path", func(t *testing.T) {
		t.Parallel()

		entry := filepath.Join("test_data", "vol.part1.rar")

		assert.Equal(t,
			[]string{entry},
			normalizeVolumes([]string{"", "."}, entry),
		)
	})
}

// TestMkDirRefusesPreExistingSymlinkDir is the Copilot finding: mkDir used to
// MkdirAll+Chtimes a path before the containment check, so a pre-existing
// symlink in the output folder was followed (empty dirs created outside, and
// the outside directory's mtime updated) even though the call then failed.
func TestMkDirRefusesPreExistingSymlinkDir(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	out := filepath.Join(tmp, "out")
	evil := filepath.Join(tmp, "evil")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.MkdirAll(evil, 0o750))

	oldTime := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	require.NoError(t, os.Chtimes(evil, oldTime, oldTime))

	require.NoError(t, os.Symlink(evil, filepath.Join(out, "sub")))

	xFile := &XFile{FilePath: "archive.zip", OutputDir: out, DirMode: 0o755}
	err = xFile.mkDir(filepath.Join(out, "sub", "nested"), 0o755, time.Now())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Stat(filepath.Join(evil, "nested"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "mkDir must not create directories through the symlink")

	info, err := os.Stat(evil)
	require.NoError(t, err)
	assert.Equal(t, oldTime.Unix(), info.ModTime().Unix(), "rejected path must not be Chtimes'd")
}

// TestOpenFlagsForExtractNameTooLongUsesExcl is the Copilot finding: Lstat of a
// too-long name fails with ENAMETOOLONG, which used to fall through to O_TRUNC.
// openFile then truncates and OpenFile-follows a raced symlink at the short name.
func TestOpenFlagsForExtractNameTooLongUsesExcl(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	long := filepath.Join(tmp, strings.Repeat("a", 300)+".txt")

	_, err := os.Lstat(long)
	if !IsErrNameTooLong(err) {
		t.Skipf("filesystem accepted a 300-byte filename (got %v)", err)
	}

	flags, usedPath, err := openFlagsForExtract(long)
	require.NoError(t, err)
	assert.Equal(t, os.O_RDWR|os.O_CREATE|os.O_EXCL, flags)
	assert.LessOrEqual(t, len(filepath.Base(usedPath)), nameMax)
}

// TestWriteExtractFileNameTooLongDoesNotFollowTruncatedSymlink plants a symlink
// at the truncated name, then writes a too-long path. The victim must stay intact.
func TestWriteExtractFileNameTooLongDoesNotFollowTruncatedSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	long := filepath.Join(tmp, strings.Repeat("a", 300)+".txt")

	_, err = os.Lstat(long)
	if !IsErrNameTooLong(err) {
		t.Skipf("filesystem accepted a 300-byte filename (got %v)", err)
	}

	victim := filepath.Join(tmp, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	short, err := TruncatePathForFS(long)
	require.NoError(t, err)
	require.NoError(t, os.Symlink(victim, short))

	require.NoError(t, writeExtractFile(long, []byte("payload"), 0o600))

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "must not follow symlink at truncated path")
}
