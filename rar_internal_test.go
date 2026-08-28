package xtractr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// tryRAR copies the attempt tracker back onto the caller's XFile. Without that
// assignment, ExtractRAR's passwordless fallback left prog on a stack copy, and
// ExtractFile's signature retry resumed from the pre-final counters.
func TestTryRARCopiesProgress(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	xFile := &XFile{
		FilePath:  filepath.Join("test_data", "symlink.rar"),
		OutputDir: t.TempDir(),
		FileMode:  DefaultFileMode,
		DirMode:   DefaultDirMode,
	}
	stale := xFile.newProgress(0, 1, 0)
	stale.Wrote = 99
	stale.Files = 7

	size, files, _, err := tryRAR(xFile, "")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	require.NotNil(t, xFile.prog)
	require.NotSame(t, stale, xFile.prog, "passwordless extract must install the attempt tracker")
	require.Equal(t, size, xFile.prog.Wrote)
}
