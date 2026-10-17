package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEnsureLinkWithinOutputRelativeDir is the filepath.Dir(".") hole: a
// relative OutputDir used to drop ".." climbs, so ../victim looked contained.
func TestEnsureLinkWithinOutputRelativeDir(t *testing.T) {
	t.Parallel()

	const output = "xtractr-rel-symlink-out"

	outside := &XFile{FilePath: "rel.cpio", OutputDir: "."}
	err := outside.ensureLinkWithinOutput("escape", "../xtractr-rel-symlink-victim")
	require.ErrorIs(t, err, ErrInvalidPath)

	nested := &XFile{FilePath: "rel.cpio", OutputDir: output}
	err = nested.ensureLinkWithinOutput(
		filepath.Join(output, "escape"),
		"../../"+output+"/victim",
	)
	require.ErrorIs(t, err, ErrInvalidPath)

	inside := &XFile{FilePath: "rel.cpio", OutputDir: output}
	err = inside.ensureLinkWithinOutput(
		filepath.Join(output, "nested", "link"),
		"../real.txt",
	)
	require.NoError(t, err)
}

// TestEnsureLinkWithinOutputFanoutBudget stops a link target that branches
// into two copies of the previous link. Depth stays small; the walk does not.
func TestEnsureLinkWithinOutputFanoutBudget(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	out := t.TempDir()
	require.NoError(t, os.Symlink(".", filepath.Join(out, "s0")))

	prev := "s0"

	for level := 1; level <= 5; level++ {
		name := fmt.Sprintf("s%d", level)
		require.NoError(t, os.Symlink(filepath.Join(prev, prev), filepath.Join(out, name)))
		prev = name
	}

	xFile := &XFile{FilePath: "fan.cpio", OutputDir: out}
	err := xFile.ensureLinkWithinOutput(filepath.Join(out, "final"), prev)
	require.ErrorIs(t, err, ErrInvalidPath)

	err = xFile.ensureLinkWithinOutput(filepath.Join(out, "ok"), "s0")
	require.NoError(t, err)
}

// TestCreateSymlinkRejectsClimbAfterComponent is the retarget hole lexical
// cleaning misses. "foo/../victim" cleans to a path inside OutputDir, and the
// walk agrees while foo is missing or deep. Creating foo -> "." afterwards
// would make the kernel resolve victim outside OutputDir.
func TestCreateSymlinkRejectsClimbAfterComponent(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	require.NoError(t, os.MkdirAll(filepath.Join(out, "sub", "deep"), 0o750))

	xFile := &XFile{FilePath: "retarget.tar", OutputDir: out}

	require.NoError(t, xFile.createSymlink(filepath.Join(out, "foo"), "sub/deep"))

	err := xFile.createSymlink(filepath.Join(out, "bar"), "foo/../../victim")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Lstat(filepath.Join(out, "bar"))
	require.ErrorIs(t, statErr, os.ErrNotExist)

	err = xFile.createSymlink(filepath.Join(out, "early"), "foo/../victim")
	require.ErrorIs(t, err, ErrInvalidPath)

	require.NoError(t, os.MkdirAll(filepath.Join(out, "nested"), 0o750))
	err = xFile.createSymlink(filepath.Join(out, "nested", "link"), "../real.txt")
	require.NoError(t, err)
}

// TestCreateSymlinkDoesNotArmPlantedClimb is a link already in OutputDir whose
// text is "missing/../victim". Pointing an archive entry at it must fail, and
// creating "missing" -> "." must be rolled back so the planted link stays inside.
func TestCreateSymlinkDoesNotArmPlantedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("missing/../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}

	err := xFile.createSymlink(filepath.Join(out, "bar"), "existing")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Lstat(filepath.Join(out, "bar"))
	require.ErrorIs(t, statErr, os.ErrNotExist)

	err = xFile.createSymlink(filepath.Join(out, "missing"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr = os.Lstat(filepath.Join(out, "missing"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// requireNotResolvingTo fails when link resolves to victim. A dangling link
// is fine: it points at nothing.
func requireNotResolvingTo(t *testing.T, link, victim string) {
	t.Helper()

	realVictim, err := filepath.EvalSymlinks(victim)
	require.NoError(t, err)

	resolved, err := filepath.EvalSymlinks(link)
	if err == nil {
		require.NotEqual(t, realVictim, resolved)
	}
}

// TestCreateSymlinkAllowsUnrelatedClimb writes a normal symlink while a
// dangling climb-after link is already in OutputDir and still resolves inside.
func TestCreateSymlinkAllowsUnrelatedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("missing/../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "readme"), "README"))

	got, err := os.Readlink(filepath.Join(out, "readme"))
	require.NoError(t, err)
	require.Equal(t, "README", got)
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// TestCreateSymlinkDoesNotArmNestedClimb is a planted climb behind another
// one: existing -> foo/../victim, where foo -> deep/../leaf. The check on
// archive text refuses to follow foo at all; the audit of planted links has
// to resolve it the way the kernel does, or creating leaf -> "." is missed.
func TestCreateSymlinkDoesNotArmNestedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(filepath.Join(out, "deep"), 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("deep/../leaf", filepath.Join(out, "foo")))
	require.NoError(t, os.Symlink("foo/../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	err := xFile.createSymlink(filepath.Join(out, "leaf"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Lstat(filepath.Join(out, "leaf"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)

	// A leaf that keeps the climb inside OutputDir is fine.
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "leaf"), "deep"))
}

// TestCreateSymlinkDoesNotArmPlantedChain is the same escape one hop further
// out: existing -> other -> missing/../victim.
func TestCreateSymlinkDoesNotArmPlantedChain(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("missing/../victim", filepath.Join(out, "other")))
	require.NoError(t, os.Symlink("other", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}

	err := xFile.createSymlink(filepath.Join(out, "bar"), "existing")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Lstat(filepath.Join(out, "bar"))
	require.ErrorIs(t, statErr, os.ErrNotExist)

	err = xFile.createSymlink(filepath.Join(out, "missing"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr = os.Lstat(filepath.Join(out, "missing"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// TestCreateHardLinkDoesNotArmPlantedClimb is a hard link to a symlink to "."
// under the missing name. That arms "missing/../victim" the same way a new
// symlink would.
func TestCreateHardLinkDoesNotArmPlantedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink(".", filepath.Join(out, "shallow")))
	require.NoError(t, os.Symlink("missing/../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	err := xFile.createHardLink(filepath.Join(out, "missing"), "shallow")
	require.ErrorIs(t, err, ErrInvalidPath)

	_, statErr := os.Lstat(filepath.Join(out, "missing"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, statErr = os.Lstat(filepath.Join(out, "shallow"))
	require.NoError(t, statErr)
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// TestCreateSymlinkIgnoresClimbAlreadyOutside still writes an unrelated
// symlink when a climb-after link already resolved outside OutputDir.
func TestCreateSymlinkIgnoresClimbAlreadyOutside(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "readme"), "README"))
}

// plantShortenedClimb lays out foo -> sub/deep and existing -> foo/../../victim.
// The text of existing cleans to a path outside OutputDir, but it resolves
// inside: the two climbs are absorbed by foo's depth. Replacing foo with a
// shorter link moves it outside.
func plantShortenedClimb(t *testing.T) (out, victim string) {
	t.Helper()

	root := t.TempDir()
	out = filepath.Join(root, "out")
	victim = filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(filepath.Join(out, "sub", "deep"), 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("sub/deep", filepath.Join(out, "foo")))
	require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

	return out, victim
}

// TestCreateSymlinkDoesNotShortenPlantedClimb replaces the link a planted
// climb goes through. The write is refused and the original link put back,
// whether or not an earlier link write already collected the planted set.
func TestCreateSymlinkDoesNotShortenPlantedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	for _, collectFirst := range []bool{true, false} {
		out, victim := plantShortenedClimb(t)
		xFile := &XFile{FilePath: "planted.tar", OutputDir: out}

		if collectFirst {
			require.NoError(t, xFile.createSymlink(filepath.Join(out, "readme"), "README"))
		}

		err := xFile.createSymlink(filepath.Join(out, "foo"), "sub")
		require.ErrorIs(t, err, ErrInvalidPath, "collect first: %v", collectFirst)

		got, err := os.Readlink(filepath.Join(out, "foo"))
		require.NoError(t, err)
		// Windows os.Symlink stores the text with backslashes.
		require.Equal(t, filepath.FromSlash("sub/deep"), got, "foo must be restored")
		requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
	}
}

// TestCreateSymlinkKeepsLookupsThroughCycle makes a planted link unresolvable
// before arming it. existing -> foo/../victim resolves through foo -> sub/deep.
// Replacing foo with foo -> bar (bar -> foo) is a cycle: no escape, but the
// lookups of that failed walk, bar among them, are what the next write must
// be matched against. bar -> . then makes existing resolve outside.
func TestCreateSymlinkKeepsLookupsThroughCycle(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(filepath.Join(out, "sub", "deep"), 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("sub/deep", filepath.Join(out, "foo")))
	require.NoError(t, os.Symlink("foo", filepath.Join(out, "bar")))
	require.NoError(t, os.Symlink("foo/../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "foo"), "bar"))

	err := xFile.createSymlink(filepath.Join(out, "bar"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	got, err := os.Readlink(filepath.Join(out, "bar"))
	require.NoError(t, err)
	require.Equal(t, "foo", got, "bar must be restored")
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// TestCreateSymlinkTracksUnresolvableLink is a planted link that does not
// resolve when collected: existing -> foo/bar/../../victim with a regular file
// at foo. Replacing foo with foo -> . would make it resolve outside, so the
// link has to be collected anyway, with the lookups it made.
func TestCreateSymlinkTracksUnresolvableLink(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(filepath.Join(out, "bar"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(out, "foo"), []byte("file"), 0o600))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("foo/bar/../../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	err := xFile.createSymlink(filepath.Join(out, "foo"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	requireFileRestored(t, filepath.Join(out, "foo"), "file")
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// requireFileRestored is the regular file a rejected link replaced: back at
// its path with its content, and nothing left aside beside it.
func requireFileRestored(t *testing.T, path, content string) {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err, "the replaced file must be put back")
	require.True(t, info.Mode().IsRegular(), "mode %s", info.Mode())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, content, string(data))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)

	for _, entry := range entries {
		require.NotContains(t, entry.Name(), asideTail, "left aside")
	}
}

// TestWalkLinkTargetStopsAtFile is ".." applied to a regular file or to a
// name that does not exist. The kernel gives ENOTDIR or ENOENT; the walk must
// not climb and report a path, and must keep the lookup it made.
func TestWalkLinkTargetStopsAtFile(t *testing.T) {
	t.Parallel()

	// The walk starts from the resolved parent; macOS puts TempDir under /var -> /private/var.
	out, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(out, "foo"), []byte("file"), 0o600))

	walk := &linkWalk{}
	_, err = walkLinkTarget(filepath.Join(out, "existing"), "foo/../../victim", walk)
	require.ErrorIs(t, err, syscall.ENOTDIR)
	require.Equal(t, []string{filepath.Join(out, "foo")}, walk.visits, "the file is still a lookup")

	walk = &linkWalk{}
	_, err = walkLinkTarget(filepath.Join(out, "existing"), "missing/../victim", walk)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.Equal(t, []string{filepath.Join(out, "missing")}, walk.visits, "the missing name is still a lookup")

	resolved, err := walkLinkTarget(filepath.Join(out, "existing"), "missing/later", &linkWalk{})
	require.NoError(t, err, "a component that does not exist yet is kept when nothing climbs out of it")
	require.Equal(t, filepath.Join(out, "missing", "later"), resolved)

	resolved, err = walkLinkTarget(filepath.Join(out, "nested", "link"), "../real.txt", &linkWalk{})
	require.NoError(t, err, "the link's own parent chain is made before the link and is climbed")
	require.Equal(t, filepath.Join(out, "real.txt"), resolved)
}

// TestCreateSymlinkTracksClimbThroughFile is a planted link whose ".." sits
// right after a regular file: existing -> foo/../../victim with a file at foo.
// A walk that climbs anyway lands outside OutputDir, and collection would drop
// the link as already outside. Replacing foo with foo -> . then makes it
// resolve outside for real.
func TestCreateSymlinkTracksClimbThroughFile(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "dir", "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(out, "foo"), []byte("file"), 0o600))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

	xFile := &XFile{FilePath: "planted.tar", OutputDir: out}
	err := xFile.createSymlink(filepath.Join(out, "foo"), ".")
	require.ErrorIs(t, err, ErrInvalidPath)

	requireFileRestored(t, filepath.Join(out, "foo"), "file")
	requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
}

// TestOutputFileByNameChecksIdentity is the guard on the pathname operations
// that follow an open: between the open and the remove or Chtimes, a link
// write can point a parent elsewhere, briefly or for good. The operation must
// land on the file that was opened or not at all.
func TestOutputFileByNameChecksIdentity(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	written, err := os.Create(path)
	require.NoError(t, err)

	info, err := written.Stat()
	require.NoError(t, err)
	require.NoError(t, written.Close())

	out := outputFile{path: path, info: info}

	// The pathname now names a different file. The written file stays alive,
	// moved aside, as it does in the race: a deleted file's inode is reused
	// at once on Linux, and that would make the two look the same.
	aside := filepath.Join(dir, "aside.txt")
	require.NoError(t, os.Rename(path, aside))
	require.NoError(t, os.WriteFile(path, []byte("someone else's"), 0o600))

	err = out.byName(os.Remove)
	require.ErrorIs(t, err, ErrInvalidPath)
	require.FileExists(t, path, "a file that is not the one written must not be removed")

	// And back to the file that was written.
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Rename(aside, path))

	out.remove()

	_, err = os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// TestCreateSymlinkReplacesEntry is the replacement archives rely on: a
// symlink, an empty directory, or a file at the path gives way to the link.
// Rejected text leaves the entry alone, and a file is never removed for it.
func TestCreateSymlinkReplacesEntry(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	out := t.TempDir()
	xFile := &XFile{FilePath: "replace.tar", OutputDir: out}

	keep := filepath.Join(out, "keep.txt")
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o600))

	err := xFile.createSymlink(keep, "../../etc/passwd")
	require.ErrorIs(t, err, ErrInvalidPath)

	data, err := os.ReadFile(keep)
	require.NoError(t, err)
	require.Equal(t, "mine", string(data))

	require.NoError(t, os.Symlink("old", filepath.Join(out, "link")))
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "link"), "new"))

	got, err := os.Readlink(filepath.Join(out, "link"))
	require.NoError(t, err)
	require.Equal(t, "new", got)

	require.NoError(t, os.Mkdir(filepath.Join(out, "empty"), 0o750))
	require.NoError(t, xFile.createSymlink(filepath.Join(out, "empty"), "new"))

	got, err = os.Readlink(filepath.Join(out, "empty"))
	require.NoError(t, err)
	require.Equal(t, "new", got)

	// A file is set aside while the link is checked, and discarded after.
	require.NoError(t, xFile.createSymlink(keep, "new"))

	got, err = os.Readlink(keep)
	require.NoError(t, err)
	require.Equal(t, "new", got)

	entries, err := os.ReadDir(out)
	require.NoError(t, err)

	for _, entry := range entries {
		require.NotContains(t, entry.Name(), asideTail, "left aside after a link replaced the file")
	}
}
