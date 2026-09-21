package xtractr

import (
	"archive/tar"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	assertNoPartials(t, dest)
}

// createSibling truncates the temp name to fit NAME_MAX, but the commit used
// to target the original overlong dest and fail with ENAMETOOLONG. The previous
// openExtractFile fallback truncated the final destination; restore that.
func TestCopyMoveNameTooLongTruncatesDest(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "payload.bin")
	dest := filepath.Join(toDir, strings.Repeat("a", 300)+".bin")

	_, err := os.Lstat(dest)
	if !IsErrNameTooLong(err) {
		t.Skipf("filesystem accepted a 300-byte filename (got %v)", err)
	}

	require.NoError(t, os.WriteFile(src, []byte("extracted"), 0o600))

	short, err := TruncatePathForFS(dest)
	require.NoError(t, err)

	require.NoError(t, copyMove(src, dest, DefaultSuffix))

	got, err := os.ReadFile(short)
	require.NoError(t, err)
	assert.Equal(t, []byte("extracted"), got)
	require.NoFileExists(t, src)
	assertNoPartials(t, dest)
	assert.LessOrEqual(t, len(filepath.Base(short)), nameMax)
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
	assertNoPartials(t, dest)
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
	assertNoPartials(t, dest)
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
	assertNoPartials(t, dest)
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
	// copyMove does not scavenge; the stale sibling is left for the next
	// moveFiles sweep, and the copy used the numbered candidate instead.
	require.FileExists(t, stale)
	require.NoFileExists(t, dest+copySiblingExt(suffix, partialTail)+".1")
	require.NoFileExists(t, dest+copySiblingExt(DefaultSuffix, partialTail))
}

func TestCopySiblingExt(t *testing.T) {
	t.Parallel()

	assert.Equal(t, ".xtractr_partial", copySiblingExt("", partialTail))
	assert.Equal(t, ".xtractr_partial", copySiblingExt(DefaultSuffix, partialTail))
	assert.Equal(t, ".unpackerred_partial", copySiblingExt("_unpackerred", partialTail))
	assert.Equal(t, ".xtractr_link", copySiblingExt(DefaultSuffix, linkTail))
}

func TestCreateSiblingSkipsTakenName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	taken := dest + copySiblingExt(DefaultSuffix, partialTail)
	require.NoError(t, os.WriteFile(taken, []byte("taken"), 0o600))

	// The taken name must not be opened/truncated; the next candidate is used.
	f, got, release, err := createSibling(dest, DefaultSuffix, 0o600)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	defer release()

	assert.Equal(t, dest+".xtractr_partial.1", got)

	// The pre-existing file is untouched.
	content, err := os.ReadFile(taken)
	require.NoError(t, err)
	assert.Equal(t, []byte("taken"), content)
}

func TestCreateSiblingKnownSuffix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "movie.mkv")

	f, got, release, err := createSibling(dest, DefaultSuffix, 0o600)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	defer release()

	assert.Equal(t, dest+".xtractr_partial", got)
}

// A planted symlink at the sibling name must not be followed or destroyed;
// the allocator skips to the next numbered candidate.
func TestCreateSiblingDoesNotFollowSymlink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	dir := t.TempDir()
	dest := filepath.Join(dir, "payload.bin")
	victim := filepath.Join(dir, "victim.bin")
	require.NoError(t, os.WriteFile(victim, []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(victim, dest+copySiblingExt(DefaultSuffix, partialTail)))

	f, got, release, err := createSibling(dest, DefaultSuffix, 0o600)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	defer release()

	assert.Equal(t, dest+".xtractr_partial.1", got)

	content, err := os.ReadFile(victim)
	require.NoError(t, err)
	assert.Equal(t, []byte("secret"), content, "must not write through symlink")
}

// Long basenames near NAME_MAX are truncated by the sibling allocator; the
// scavenger anchors on the tail, so truncated and numbered-truncated
// leftovers are still cleaned up.
func TestScavengePartialsLongName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	longBase := strings.Repeat("a", nameMax)
	dest := filepath.Join(dir, longBase)

	// Create leftovers with the exact truncated names the allocator would use.
	_, stem, fullTail := siblingStem(dest, DefaultSuffix, partialTail)
	stale := filepath.Join(dir, stem+fullTail)
	staleN := siblingCandidate(dir, stem, fullTail, 1)

	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o600))
	require.NoError(t, os.WriteFile(staleN, []byte("stale.1"), 0o600))

	scavengePartials(dir, DefaultSuffix)
	require.NoFileExists(t, stale, "truncated-stem partial must be scavenged")
	require.NoFileExists(t, staleN, "numbered truncated-stem partial must be scavenged")
}

// The scavenger removes only allocator-generated names: the bare branded tail
// and numeric .N variants, for both partial and link tails. Lookalikes and
// real files are left alone. Trailing-dot names (empty postfix) are covered by
// TestIsCopySibling; Win32 strips trailing dots so they cannot be created here.
func TestScavengePartialsExactMatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	remove := []string{
		"a.mkv.xtractr_partial",
		"a.mkv.xtractr_partial.1",
		"a.mkv.xtractr_partial.1000",
		"a.mkv.xtractr_link",
		"a.mkv.xtractr_link.2",
	}
	keep := []string{
		"a.mkv",                        // real file
		"a.mkv.xtractr_partial.backup", // non-numeric suffix
		"a.mkv.xtractr_partial.1.5",    // not a single numeric postfix
		".xtractr_partial",             // empty stem is never generated
		"a.mkv.other_partial",          // different brand
		"a.mkv.xtractr_partial.old.1",  // tail not final
	}

	for _, name := range append(append([]string{}, remove...), keep...) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	scavengePartials(dir, DefaultSuffix)

	for _, name := range remove {
		require.NoFileExists(t, filepath.Join(dir, name), "should scavenge %s", name)
	}

	for _, name := range keep {
		require.FileExists(t, filepath.Join(dir, name), "should keep %s", name)
	}
}

func TestIsCopySibling(t *testing.T) {
	t.Parallel()

	assert.True(t, isCopySibling("a.mkv.xtractr_partial", DefaultSuffix))
	assert.True(t, isCopySibling("a.mkv.xtractr_partial.1", DefaultSuffix))
	assert.True(t, isCopySibling("a.mkv.xtractr_link.2", DefaultSuffix))
	assert.False(t, isCopySibling("a.mkv", DefaultSuffix))
	assert.False(t, isCopySibling("a.mkv.xtractr_partial.backup", DefaultSuffix))
	assert.False(t, isCopySibling("a.mkv.xtractr_partial.1.5", DefaultSuffix))
	assert.False(t, isCopySibling("a.mkv.xtractr_partial.", DefaultSuffix), "empty postfix is not numeric")
	assert.False(t, isCopySibling(".xtractr_partial", DefaultSuffix), "empty stem is never generated")
	assert.False(t, isCopySibling("a.mkv.other_partial", DefaultSuffix))
	assert.False(t, isCopySibling("a.mkv.xtractr_partial.old.1", DefaultSuffix))
}

// A sibling registered by an in-flight copy must survive the scavenger; once
// released (renamed away or removed), it becomes scavengable again.
func TestScavengeSkipsActiveSibling(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dest := filepath.Join(dir, "movie.mkv")

	file, partial, release, err := createSibling(dest, DefaultSuffix, 0o600)
	require.NoError(t, err)

	defer release() // a second release is a harmless map delete

	_, err = file.WriteString("still copying")
	require.NoError(t, err)

	scavengePartials(dir, DefaultSuffix)
	require.FileExists(t, partial, "in-flight partial must survive the sweep")

	require.NoError(t, file.Close())
	release()

	scavengePartials(dir, DefaultSuffix)
	require.NoFileExists(t, partial, "released partial is scavenged")
}

// Parallel moves into the same destination directory must all succeed; the
// registry (not a move-wide lock) is what keeps their siblings and scavenges
// from interfering. Run under -race to catch unsynchronized registry access.
func TestMoveFilesConcurrentSameDest(t *testing.T) {
	t.Parallel()

	toDir := t.TempDir()

	const workers = 4

	var waitGrp sync.WaitGroup

	errs := make([]error, workers)

	for idx := range workers {
		fromDir := t.TempDir()
		name := fmt.Sprintf("file%d.bin", idx)
		require.NoError(t, os.WriteFile(filepath.Join(fromDir, name), []byte(fmt.Sprintf("data%d", idx)), 0o600))

		waitGrp.Go(func() {
			_, errs[idx] = moveFiles(NoLogger(), 0o755, fromDir, toDir, false, "")
		})
	}

	waitGrp.Wait()

	for idx := range workers {
		require.NoError(t, errs[idx])

		content, err := os.ReadFile(filepath.Join(toDir, fmt.Sprintf("file%d.bin", idx)))
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("data%d", idx), string(content))
	}
}

// FUSE (e.g. Unraid shfs) can return an empty ReadDir after a successful write
// while Lstat of those same paths still works. The extract write list is the
// source of truth for what to move.
func TestMoveSourcesUnionsKnownWhenListingEmpty(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	hidden := filepath.Join(fromDir, "movie.mkv")
	nestedDir := filepath.Join(fromDir, "dir")
	nested := filepath.Join(nestedDir, "inner.bin")
	outside := filepath.Join(t.TempDir(), "evil.bin")

	require.NoError(t, os.WriteFile(hidden, []byte("payload"), 0o600))
	require.NoError(t, os.Mkdir(nestedDir, 0o750))
	require.NoError(t, os.WriteFile(nested, []byte("inner"), 0o600))
	require.NoError(t, os.WriteFile(outside, []byte("nope"), 0o600))

	got, err := moveSources(fromDir, nil, []string{hidden, nested, nested, outside, "", fromDir})
	require.NoError(t, err)
	assert.Equal(t, []string{hidden, nestedDir}, got)
}

func TestMoveSourcesUnionsListingWithKnown(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	listed := filepath.Join(fromDir, "sample")
	missed := filepath.Join(fromDir, "movie.mkv")

	require.NoError(t, os.Mkdir(listed, 0o750))
	require.NoError(t, os.WriteFile(missed, []byte("payload"), 0o600))

	got, err := moveSources(fromDir, []string{listed}, []string{missed, listed})
	require.NoError(t, err)
	assert.Equal(t, []string{listed, missed}, got)
}

func TestMoveSourcesEmptyListingWithoutKnownIsOK(t *testing.T) {
	t.Parallel()

	got, err := moveSources(t.TempDir(), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestMoveSourcesEmptyListingKnownMissingErrors(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	_, err := moveSources(fromDir, nil, []string{filepath.Join(fromDir, "movie.mkv")})
	require.ErrorIs(t, err, errExtractListingEmpty)
}

func TestMoveFilesKnownEmptyListingLeavesTemp(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	known := filepath.Join(fromDir, "movie.mkv")

	_, err := moveFilesKnown(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix, []string{known})
	require.ErrorIs(t, err, errExtractListingEmpty)
	require.DirExists(t, fromDir, "temp dir must survive when the write list is not visible")
	require.NoFileExists(t, filepath.Join(toDir, "movie.mkv"))
}

func TestMoveFilesKnownMovesWriteList(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "movie.mkv")
	dest := filepath.Join(toDir, "movie.mkv")

	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	got, err := moveFilesKnown(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix, []string{src})
	require.NoError(t, err)
	require.Equal(t, []string{dest}, got.NewFiles)

	content, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), content)
	require.NoDirExists(t, fromDir)
}

func TestMoveSourcesRelativeName(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	hidden := filepath.Join(fromDir, "movie.mkv")
	require.NoError(t, os.WriteFile(hidden, []byte("payload"), 0o600))

	got, err := moveSources(fromDir, nil, []string{"movie.mkv"})
	require.NoError(t, err)
	assert.Equal(t, []string{hidden}, got)
}

func TestMoveSourcesPartialMissErrors(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	present := filepath.Join(fromDir, "movie.mkv")
	require.NoError(t, os.WriteFile(present, []byte("payload"), 0o600))

	_, err := moveSources(fromDir, []string{present}, []string{present, filepath.Join(fromDir, "gone.mkv")})
	require.ErrorIs(t, err, errExtractListingEmpty)
}

func TestMoveFilesKnownPartialMissLeavesTemp(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()
	src := filepath.Join(fromDir, "movie.mkv")
	require.NoError(t, os.WriteFile(src, []byte("payload"), 0o600))

	_, err := moveFilesKnown(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix,
		[]string{src, filepath.Join(fromDir, "other.mkv")})
	require.ErrorIs(t, err, errExtractListingEmpty)
	require.DirExists(t, fromDir)
	require.FileExists(t, src)
	require.NoFileExists(t, filepath.Join(toDir, "movie.mkv"))
}

func TestWithoutPathsDropsDeletedNestedArchive(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	payload := filepath.Join(fromDir, "payload.txt")
	nested := filepath.Join(fromDir, "inner.zip")

	require.NoError(t, os.WriteFile(payload, []byte("kept"), 0o600))

	toDir := t.TempDir()
	known := withoutPaths(resolveExtractPaths(fromDir, []string{payload, "inner.zip"}), []string{nested})
	got, err := moveFilesKnown(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix, known)
	require.NoError(t, err)
	require.Equal(t, []string{filepath.Join(toDir, "payload.txt")}, got.NewFiles)
	require.NoDirExists(t, fromDir)
}

func TestTarWriteListSurvivesEmptyListing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tarPath := filepath.Join(dir, "movie.tar")
	writeTarFile(t, tarPath, "dir/movie.mkv", []byte("payload"))

	out := filepath.Join(dir, "out")
	_, files, err := ExtractTar(&XFile{
		FilePath:  tarPath,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o750,
	})
	require.NoError(t, err)

	got, err := moveSources(out, nil, files)
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join(out, "dir")}, got)
}

func writeTarFile(t *testing.T, path, name string, body []byte) {
	t.Helper()

	file, err := os.Create(path)
	require.NoError(t, err)

	writer := tar.NewWriter(file)
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o600,
		Size: int64(len(body)),
	}))
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
}

func TestMoveFilesEmptySourceStillDeletes(t *testing.T) {
	t.Parallel()

	fromDir := t.TempDir()
	toDir := t.TempDir()

	got, err := moveFiles(NoLogger(), 0o755, fromDir, toDir, false, DefaultSuffix)
	require.NoError(t, err)
	assert.Empty(t, got.NewFiles)
	require.NoDirExists(t, fromDir, "empty extract with no write list still removes the temp dir")
}

func assertNoPartials(t *testing.T, dest string) {
	t.Helper()

	dir, stem, fullTail := siblingStem(dest, DefaultSuffix, partialTail)
	prefix := stem + fullTail
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		name := entry.Name()
		assert.False(t, name == prefix || strings.HasPrefix(name, prefix+"."),
			"leftover partial %s", name)
	}
}
