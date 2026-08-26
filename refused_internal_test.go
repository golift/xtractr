package xtractr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFileMover() *Xtractr {
	return &Xtractr{config: &Config{Logger: NoLogger(), DirMode: 0o755}}
}

func TestMoveFilesRefusesOccupiedFile(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false)
	require.NoError(t, err)
	assert.Empty(t, got.NewFiles)
	require.Len(t, got.Refused, 1)
	assert.Equal(t, src, got.Refused[0].Src)
	assert.Equal(t, dest, got.Refused[0].Dest)

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("existing"), content)

	_, err = os.Stat(fromDir)
	require.ErrorIs(t, err, os.ErrNotExist, "temp folder must still be deleted after a refusal")
}

func TestMoveFilesRefusesOccupiedDirectory(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.Mkdir(dest, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("still here"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false)
	require.NoError(t, err)
	require.Len(t, got.Refused, 1)
	assert.Equal(t, src, got.Refused[0].Src)
	assert.Equal(t, dest, got.Refused[0].Dest)

	content, err := os.ReadFile(filepath.Join(dest, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("still here"), content)
}

func TestMoveFilesOverwriteRecordsNothing(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	dest := filepath.Join(toDir, "payload.bin")
	src := filepath.Join(fromDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, true)
	require.NoError(t, err)
	assert.Empty(t, got.Refused)
	require.Equal(t, []string{dest}, got.NewFiles)

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), content)

	_, err = os.Stat(src)
	require.ErrorIs(t, err, os.ErrNotExist, "overwrite must still remove the temp copy")
}

func TestRenameFilesReportsRefused(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))

	renamed, err := testFileMover().RenameFiles(fromDir, toDir, false)
	require.NoError(t, err)
	assert.Empty(t, renamed.NewFiles)
	require.Len(t, renamed.Refused, 1)
	assert.Equal(t, src, renamed.Refused[0].Src)
	assert.Equal(t, dest, renamed.Refused[0].Dest)
}

func TestMoveFilesOmitsRefused(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(fromDir, "payload.bin"), []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(toDir, "payload.bin"), []byte("existing"), 0o600))

	files, err := testFileMover().MoveFiles(fromDir, toDir, false)
	require.NoError(t, err)
	assert.Empty(t, files)
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()

	err := os.Symlink("target", filepath.Join(t.TempDir(), "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
}

func TestMoveFilesRefusesDanglingSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(toDir, "missing-target"), dest))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false)
	require.NoError(t, err)
	assert.Empty(t, got.NewFiles)
	require.Len(t, got.Refused, 1)
	assert.Equal(t, dest, got.Refused[0].Dest)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
}

func TestMoveFilesRefusesLiveSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	victim := filepath.Join(toDir, "victim.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(victim, dest))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false)
	require.NoError(t, err)
	require.Len(t, got.Refused, 1)

	content, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), content)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink)
}

func TestMoveFilesOverwriteReplacesDanglingSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(toDir, "missing-target"), dest))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, true)
	require.NoError(t, err)
	assert.Empty(t, got.Refused)
	require.Equal(t, []string{dest}, got.NewFiles)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), content)
}

func TestMoveFilesOverwriteDoesNotFollowLiveSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	victim := filepath.Join(toDir, "victim.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(victim, dest))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, true)
	require.NoError(t, err)
	assert.Empty(t, got.Refused)

	content, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), content, "must not follow dest symlink")

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	gotDest, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), gotDest)
}

func TestMoveFilesStatErrorIsNotARefusal(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))

	// Lstat of a path under a regular file fails with ENOTDIR, not ErrNotExist.
	// With overwrite=false that must surface as an error, not a false refusal.
	notADir := filepath.Join(toDir, "not-a-dir")
	require.NoError(t, os.WriteFile(notADir, []byte("file"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, filepath.Join(notADir, "sub"), false)
	require.Error(t, err)
	assert.Empty(t, got.Refused, "a stat error is not an occupied destination")
}

func TestMoveFilesOverwriteDoesNotFollowDirSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	realDir := filepath.Join(toDir, "real")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.Mkdir(realDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "keep.txt"), []byte("still here"), 0o600))
	require.NoError(t, os.Symlink(realDir, dest))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, true)
	require.NoError(t, err)
	assert.Empty(t, got.Refused)

	content, err := os.ReadFile(filepath.Join(realDir, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("still here"), content)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	gotDest, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), gotDest)
}

// Copilot: a move failure must not delete the temp source. Before this fix,
// the unconditional deleteFiles(fromPath) ran even when keepErr was set,
// destroying the extracted data along with the partial destination result.
func TestMoveFilesMoveErrorPreservesSource(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()

	src := filepath.Join(fromDir, "payload.bin")
	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))

	// Dest is a non-empty directory. os.Rename(file, nonemptydir) fails, so
	// keepErr is set in the loop while fromPath still holds the source.
	dest := filepath.Join(toDir, "payload.bin")
	require.NoError(t, os.Mkdir(dest, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(dest, "keep.txt"), []byte("keep"), 0o600))

	_, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, true)
	require.Error(t, err)

	// The temp source must survive so the data is recoverable.
	require.FileExists(t, src)
	require.DirExists(t, fromDir)
}

// A refusal is not an error: the destination is complete (occupied), so the
// temp source is still cleaned up. Only genuine move failures preserve it.
func TestMoveFilesRefusalStillCleansSource(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false)
	require.NoError(t, err)
	require.Len(t, got.Refused, 1)

	// The moved (refused) file's temp copy is gone; fromPath itself is removed.
	require.NoDirExists(t, fromDir)
}
