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
