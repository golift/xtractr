package xtractr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractISOSharedBudgetOpenFailureDoesNotPanic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	isoPath := filepath.Join(dir, "truncated.iso")
	require.NoError(t, os.WriteFile(isoPath, []byte("not an iso"), 0o600))

	xFile := &XFile{
		FilePath:  isoPath,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  DefaultFileMode,
		DirMode:   DefaultDirMode,
		prog:      newSharedBudget(),
	}

	require.NotPanics(t, func() {
		_, _, err := ExtractISO(xFile)
		require.Error(t, err)
	})
}

func TestProgressTrackerDoneNilSafe(t *testing.T) {
	t.Parallel()

	var tracker *progressTracker
	require.NotPanics(t, tracker.done)

	tracker = &progressTracker{}
	require.NotPanics(t, tracker.done)
	require.True(t, tracker.Done)
}
