package xtractr

import (
	"os"
	"path/filepath"
	"strings"
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

func TestUDFOpenFailureSurfacesReaderError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	isoPath := filepath.Join(dir, "disc.iso")
	// Sector 16 must be complete or iso9660 returns EOF before it can see BEA01.
	buf := make([]byte, 0x8000+0x800)
	copy(buf[0x8001:], "BEA01")
	require.NoError(t, os.WriteFile(isoPath, buf, 0o600))

	_, archiveType, err := detectBySignature(isoPath)
	require.NoError(t, err)
	require.Equal(t, "iso", archiveType)

	xFile := &XFile{
		FilePath:  isoPath,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  DefaultFileMode,
		DirMode:   DefaultDirMode,
	}

	_, _, _, err = ExtractFile(xFile) //nolint:dogsled // only the error matters here.
	require.Error(t, err)

	msg := err.Error()
	require.Contains(t, msg, "failed to open UDF image")
	require.Contains(t, msg, "anchor")
	require.NotContains(t, msg, "UDF volumes are not supported")
	require.NotContains(t, msg, "unknown archive file type")
	require.Equal(t, 1, strings.Count(msg, isoPath))
}

func TestProgressTrackerDoneNilSafe(t *testing.T) {
	t.Parallel()

	var tracker *progressTracker
	require.NotPanics(t, tracker.done)

	tracker = &progressTracker{}
	require.NotPanics(t, tracker.done)
	require.True(t, tracker.Done)
}
