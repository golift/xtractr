package xtractr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyMoveRegularFile(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), got)
	require.NoFileExists(t, src)
	assertNoPartials(t, dest, DefaultSuffix)
}

func TestCopyMoveSymlinkDest(t *testing.T) {
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

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	got, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), got, "must not follow dest symlink")

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	gotDest, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), gotDest)
	assertNoPartials(t, dest, DefaultSuffix)
}

func TestCopyMoveDanglingSymlinkDest(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	missing := filepath.Join(toDir, "missing-target")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.Symlink(missing, dest))

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	_, err := os.Lstat(missing)
	require.ErrorIs(t, err, os.ErrNotExist, "must not create the dangling target")

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), got)
}

func TestCopyMoveSymlinkSource(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	fromDir := t.TempDir()
	toDir := t.TempDir()
	target := filepath.Join(fromDir, "real.bin")
	src := filepath.Join(fromDir, "link.bin")
	dest := filepath.Join(toDir, "link.bin")

	require.NoError(t, os.WriteFile(target, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(target, src))

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeSymlink)

	got, err := os.Readlink(dest)
	require.NoError(t, err)
	assert.Equal(t, target, got)

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), content, "must not copy the symlink target")
	require.NoFileExists(t, src)
}

func TestCopyMoveHardlinkDest(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	other := filepath.Join(toDir, "other.bin")

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(other, []byte("original"), 0o600))

	err := os.Link(other, dest)
	if err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), got)

	kept, err := os.ReadFile(other)
	require.NoError(t, err)
	assert.Equal(t, []byte("original"), kept, "rename-over must sever the hard link")
}

func TestCopyMoveDirectorySourceLeavesDest(t *testing.T) {
	t.Parallel()

	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "payload.bin")
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))

	err := copyMove(src, dest, DefaultSuffix)
	require.Error(t, err)

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("existing"), got)
	assertNoPartials(t, dest, DefaultSuffix)
}

func TestMoveFilesScavengesPartials(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	stale := dest + copySiblingExt(DefaultSuffix, partialTail)
	staleN := dest + copySiblingExt(DefaultSuffix, partialTail) + ".1"

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))
	require.NoError(t, os.WriteFile(staleN, []byte("stale.1"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix)
	require.NoError(t, err)
	require.Equal(t, []string{dest}, got.NewFiles)

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), content)
	assertNoPartials(t, dest, DefaultSuffix)
}

// A stale partial is safe to remove even when the destination is occupied:
// the partial is xtractr's own remnant, never the pre-existing download file.
func TestMoveFilesScavengesPartialWhileOccupied(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")
	stale := dest + copySiblingExt(DefaultSuffix, partialTail)

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(dest, []byte("existing"), 0o600))
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix)
	require.NoError(t, err)
	require.Len(t, got.Refused, 1, "occupied destination must still refuse")

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("existing"), content, "dest file must be untouched")
	require.NoFileExists(t, stale, "stale partial is scavenged")
}

func TestCopyMoveCustomSuffix(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, "payload.bin")

	const suffix = "_unpackerred"

	stale := dest + copySiblingExt(suffix, partialTail)

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))

	require.NoError(t, copyMove(src, dest, suffix))

	got, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), got)
	assertNoPartials(t, dest, suffix)
	require.NoFileExists(t, dest+copySiblingExt(DefaultSuffix, partialTail))
}

func TestCopySiblingExt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ".xtractr_partial", copySiblingExt("", partialTail))
	assert.Equal(t, ".xtractr_partial", copySiblingExt(DefaultSuffix, partialTail))
	assert.Equal(t, ".unpackerred_partial", copySiblingExt("_unpackerred", partialTail))
	assert.Equal(t, ".xtractr_link", copySiblingExt(DefaultSuffix, linkTail))
}

func TestUnusedSiblingSkipsTakenName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	taken := dest + copySiblingExt(DefaultSuffix, partialTail)
	require.NoError(t, os.WriteFile(taken, []byte("taken"), 0o600))

	got, err := unusedSibling(dest, DefaultSuffix, partialTail)
	require.NoError(t, err)
	assert.Equal(t, dest+".xtractr_partial.1", got)
}

func TestUnusedSiblingKnownSuffix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "movie.mkv")

	got, err := unusedSibling(dest, DefaultSuffix, partialTail)
	require.NoError(t, err)
	assert.Equal(t, dest+".xtractr_partial", got)
}

func assertNoPartials(t *testing.T, dest, suffix string) {
	t.Helper()

	prefix := filepath.Base(dest) + copySiblingExt(suffix, partialTail)
	entries, err := os.ReadDir(filepath.Dir(dest))
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		assert.False(t, name == prefix || strings.HasPrefix(name, prefix+"."),
			"leftover partial %s", name)
	}
}
