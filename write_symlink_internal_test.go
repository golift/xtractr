package xtractr

import (
	"fmt"
	"os"
	"path/filepath"
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
		require.Equal(t, "sub/deep", got, "foo must be restored")
		requireNotResolvingTo(t, filepath.Join(out, "existing"), victim)
	}
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
}
