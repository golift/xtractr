package xtractr

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
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

// TestOpenExtractFileNameTooLongCreatesShortName is the Copilot finding: Lstat of a
// too-long name used to fall through to O_TRUNC. The exclusive-create path now
// retries the truncated name with O_EXCL / CREATE_NEW instead.
func TestOpenExtractFileNameTooLongCreatesShortName(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	long := filepath.Join(tmp, strings.Repeat("a", 300)+".txt")

	_, err := os.Lstat(long)
	if !IsErrNameTooLong(err) {
		t.Skipf("filesystem accepted a 300-byte filename (got %v)", err)
	}

	fout, usedPath, err := openExtractFile(long, 0o600)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(filepath.Base(usedPath)), nameMax)
	require.NoError(t, fout.Close())

	info, err := os.Lstat(usedPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
	assert.True(t, info.Mode().IsRegular())
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

func TestOpenExtractFileReplacesSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(tmp, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	dest := filepath.Join(tmp, "track.flac")
	require.NoError(t, os.Symlink(victim, dest))

	fout, usedPath, err := openExtractFile(dest, 0o600)
	require.NoError(t, err)
	assert.Equal(t, dest, usedPath)

	_, err = fout.WriteString("track")
	require.NoError(t, err)
	require.NoError(t, fout.Close())

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "must not follow destPath symlink")

	written, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("track"), written)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
}

func TestOpenExtractFileReplacesDirectorySymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	dir := filepath.Join(tmp, "dir")
	require.NoError(t, os.Mkdir(dir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "inside"), []byte("keep"), 0o600))

	dest := filepath.Join(tmp, "member")
	require.NoError(t, os.Symlink(dir, dest))

	fout, usedPath, err := openExtractFile(dest, 0o600)
	require.NoError(t, err)
	assert.Equal(t, dest, usedPath)

	_, err = fout.WriteString("payload")
	require.NoError(t, err)
	require.NoError(t, fout.Close())

	got, err := os.ReadFile(filepath.Join(dir, "inside"))
	require.NoError(t, err)
	assert.Equal(t, []byte("keep"), got, "must not follow a directory symlink/junction")

	written, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), written)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
	assert.True(t, info.Mode().IsRegular())
}

func TestOpenExtractFileRefusesDirectory(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	marker := filepath.Join(dest, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0o600))

	_, _, err := openExtractFile(dest, 0o600)
	require.Error(t, err)

	got, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, []byte("keep"), got)
}

func TestOpenFileNoFollowDoesNotFollowSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(tmp, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	dest := filepath.Join(tmp, "track.flac")
	require.NoError(t, os.Symlink(victim, dest))

	_, err = openFileNoFollow(dest, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	require.Error(t, err)
	require.ErrorIs(t, err, errExtractSymlink)

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "O_TRUNC must not follow a final-component symlink")

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink, "symlink should still be present")
}

func TestOpenExtractFileTruncatesRegularFile(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "track.flac")
	require.NoError(t, os.WriteFile(dest, []byte("old payload"), 0o600))

	fout, usedPath, err := openExtractFile(dest, 0o600)
	require.NoError(t, err)
	assert.Equal(t, dest, usedPath)

	_, err = fout.WriteString("new")
	require.NoError(t, err)
	require.NoError(t, fout.Close())

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("new"), got)
}

// TestOpenExtractFileRetriesWhenOpenSeesSymlink is the qwen finding: Lstat (or
// exclusive-create EEXIST) can observe a regular file while the no-follow open
// of that name sees a symlink planted in the window. The retry must unlink and
// exclusive-create, never write through the link.
func TestOpenExtractFileRetriesWhenOpenSeesSymlink(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	victim := filepath.Join(tmp, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))

	dest := filepath.Join(tmp, "track.flac")
	require.NoError(t, os.WriteFile(dest, []byte("old"), 0o600))

	calls := 0
	opener := func(path string, flags int, mode os.FileMode) (*os.File, error) {
		calls++

		if flags&os.O_EXCL != 0 && calls == 1 {
			return nil, os.ErrExist
		}

		if flags&os.O_EXCL == 0 && calls == 2 {
			require.NoError(t, os.Remove(dest))
			require.NoError(t, os.Symlink(victim, dest))

			return nil, errExtractSymlink
		}

		return openFileNoFollow(path, flags, mode)
	}

	fout, usedPath, err := openExtractFileWith(opener, dest, 0o600)
	require.NoError(t, err)
	assert.Equal(t, dest, usedPath)
	assert.GreaterOrEqual(t, calls, 3)

	_, err = fout.WriteString("track")
	require.NoError(t, err)
	require.NoError(t, fout.Close())

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "retry must not follow the planted symlink")

	written, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("track"), written)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
}

func TestOpenExtractFileRetriesWhenFileVanishes(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "track.flac")
	calls := 0
	opener := func(path string, flags int, mode os.FileMode) (*os.File, error) {
		calls++

		if flags&os.O_EXCL != 0 && calls == 1 {
			return nil, os.ErrExist
		}

		if flags&os.O_EXCL == 0 && calls == 2 {
			return nil, os.ErrNotExist
		}

		return openFileNoFollow(path, flags, mode)
	}

	fout, usedPath, err := openExtractFileWith(opener, dest, 0o600)
	require.NoError(t, err)
	assert.Equal(t, dest, usedPath)
	assert.GreaterOrEqual(t, calls, 3)
	require.NoError(t, fout.Close())

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

func TestOpenExtractFileConflictExhaustion(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "track.flac")
	opener := func(string, int, os.FileMode) (*os.File, error) {
		return nil, errExtractSymlink
	}

	_, _, err := openExtractFileWith(opener, dest, 0o600)
	require.ErrorIs(t, err, errExtractConflict)
	require.ErrorIs(t, err, errExtractSymlink)
}

func TestOpenExtractFileNotExistExhaustion(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "track.flac")
	opener := func(string, int, os.FileMode) (*os.File, error) {
		return nil, os.ErrNotExist
	}

	_, _, err := openExtractFileWith(opener, dest, 0o600)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotErrorIs(t, err, errExtractConflict)
}

func TestMoveFilesUsesProvidedDirMode(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission bits")
	}

	fromDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fromDir, "a.txt"), []byte("hi"), 0o600))

	dest := filepath.Join(t.TempDir(), "dest")
	_, err := moveFiles(NoLogger(), 0o700, fromDir, dest, false)
	require.NoError(t, err)

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"DirMode must reach MkdirAll when the dest does not already exist")
}

func TestBindMoveFilesUsesXFileDirMode(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission bits")
	}

	fromDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fromDir, "a.txt"), []byte("hi"), 0o600))

	xFile := &XFile{
		DirMode: 0o700,
		log:     NoLogger(),
	}
	xFile.bindMoveFiles()

	dest := filepath.Join(t.TempDir(), "dest")
	_, err := xFile.moveFiles(fromDir, dest, false)
	require.NoError(t, err)

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestMoveFilesZeroDirModeUsesDefault(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permission bits")
	}

	fromDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fromDir, "a.txt"), []byte("hi"), 0o600))

	dest := filepath.Join(t.TempDir(), "dest")
	_, err := moveFiles(NoLogger(), 0, fromDir, dest, false)
	require.NoError(t, err)

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(DefaultDirMode).Perm(), info.Mode().Perm())
}

func TestSquashRootLeavesSingleFile(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	path := filepath.Join(out, "a.txt")
	require.NoError(t, os.WriteFile(path, []byte("hi"), 0o600))

	xFile := &XFile{
		OutputDir:  out,
		SquashRoot: true,
		log:        NoLogger(),
	}
	xFile.bindMoveFiles()

	got, err := xFile.squashRoot([]string{path})
	require.NoError(t, err)
	assert.Equal(t, []string{path}, got)

	_, err = os.Stat(path)
	require.NoError(t, err, "SquashRoot must not delete a lone extracted file")
}

func TestSquashRootMovesSingleDirectory(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	nested := filepath.Join(out, "root")
	require.NoError(t, os.Mkdir(nested, 0o700))
	inner := filepath.Join(nested, "a.txt")
	require.NoError(t, os.WriteFile(inner, []byte("hi"), 0o600))

	xFile := &XFile{
		OutputDir:  out,
		SquashRoot: true,
		DirMode:    0o755,
		log:        NoLogger(),
	}
	xFile.bindMoveFiles()

	got, err := xFile.squashRoot([]string{inner})
	require.NoError(t, err)

	dest := filepath.Join(out, "a.txt")
	assert.Equal(t, []string{dest}, got)
	require.FileExists(t, dest)

	_, err = os.Stat(nested)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMoveFilesDoesNotDeleteSourceFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "a.txt")
	require.NoError(t, os.WriteFile(src, []byte("hi"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, src, dir, false)
	require.NoError(t, err)
	assert.Equal(t, []string{src}, got)
	require.FileExists(t, src)
}

func TestMkDirCountsEachMissingComponent(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	xFile := &XFile{FilePath: "a.zip", OutputDir: out, MaxFiles: 2, DirMode: 0o755}
	xFile.newProgress(0, 0, 0)

	err := xFile.mkDir(filepath.Join(out, "a", "b", "c"), 0o755, time.Now())
	require.ErrorIs(t, err, ErrMaxFiles)
	require.DirExists(t, filepath.Join(out, "a"))
	require.DirExists(t, filepath.Join(out, "a", "b"))
	require.NoDirExists(t, filepath.Join(out, "a", "b", "c"))
}

func TestCreateSymlinkRemovesWhenMaxFilesExceeded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := os.Symlink("target", filepath.Join(dir, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	xFile := &XFile{FilePath: "a.zip", OutputDir: dir, MaxFiles: 1, DirMode: 0o755}
	xFile.newProgress(0, 0, 0)
	require.NoError(t, xFile.countExtracted())

	link := filepath.Join(dir, "link")
	err = xFile.createSymlink(link, "target")
	require.ErrorIs(t, err, ErrMaxFiles)

	_, statErr := os.Lstat(link)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestCreateHardLinkRemovesWhenMaxFilesExceeded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real"), []byte("x"), 0o600))

	xFile := &XFile{FilePath: "a.zip", OutputDir: dir, MaxFiles: 1, DirMode: 0o755}
	xFile.newProgress(0, 0, 0)
	require.NoError(t, xFile.countExtracted())

	link := filepath.Join(dir, "link")

	err := xFile.createHardLink(link, "real")
	if err != nil && !errors.Is(err, ErrMaxFiles) {
		t.Skipf("hard links and symlink fallback unavailable: %v", err)
	}

	require.ErrorIs(t, err, ErrMaxFiles)

	_, statErr := os.Lstat(link)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
