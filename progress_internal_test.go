package xtractr

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountedWriteSeekerCountsGrowthOnly(t *testing.T) {
	t.Parallel()

	outFile, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outFile.Close() })

	xFile := &XFile{MaxBytes: 100}
	xFile.newProgress(0, 100, 0)
	writer := xFile.countedWriteSeeker(outFile)

	_, err = writer.Write(make([]byte, 20))
	require.NoError(t, err)
	require.Equal(t, uint64(20), xFile.prog.Wrote)

	_, err = writer.Seek(0, io.SeekStart)
	require.NoError(t, err)

	_, err = writer.Write(make([]byte, 8))
	require.NoError(t, err)
	require.Equal(t, uint64(20), xFile.prog.Wrote, "StreamInfo-style overwrite must not recount")

	_, err = writer.Seek(0, io.SeekEnd)
	require.NoError(t, err)

	_, err = writer.Write(make([]byte, 5))
	require.NoError(t, err)
	require.Equal(t, uint64(25), xFile.prog.Wrote)
}

func TestCountedWriteSeekerStopsAtMaxBytes(t *testing.T) {
	t.Parallel()

	outFile, err := os.Create(filepath.Join(t.TempDir(), "out"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = outFile.Close() })

	xFile := &XFile{MaxBytes: 10}
	xFile.newProgress(0, 100, 0)
	writer := xFile.countedWriteSeeker(outFile)

	_, err = writer.Write(make([]byte, 8))
	require.NoError(t, err)

	_, err = writer.Write(make([]byte, 8))
	require.ErrorIs(t, err, ErrMaxBytes)
	require.Equal(t, uint64(8), xFile.prog.Wrote)
}

func TestContinueArchiveProgressKeepsCounts(t *testing.T) {
	t.Parallel()

	xFile := &XFile{FilePath: "a.iso", MaxBytes: 1000, MaxFiles: 20}
	first := xFile.newArchiveProgress(0, 10, 0)
	first.Wrote = 40
	first.Files = 3

	second := xFile.continueArchiveProgress(50, 10, 2)
	require.Equal(t, uint64(40), second.Wrote)
	require.Equal(t, 3, second.Files)
}

func TestArchiveProgressReturnsClaimedMaxFiles(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxFiles: 1}
	_, err := xFile.archiveProgress(0, 10, 5)
	require.ErrorIs(t, err, ErrMaxFiles)
}

func TestArchiveProgressKeepsCountsAcrossCalls(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxFiles: 100}
	first, err := xFile.archiveProgress(0, 10, 1)
	require.NoError(t, err)

	first.Files = 7

	attempt := *xFile
	_, err = attempt.archiveProgress(0, 10, 1)
	require.NoError(t, err)
	require.Equal(t, 7, attempt.prog.Files)
}

func TestExceedsRatioFailsClosedWithoutCompressedSize(t *testing.T) {
	t.Parallel()

	require.False(t, exceedsRatio(100, 0, 0), "MaxRatio 0 is unlimited")
	require.False(t, exceedsRatio(0, 0, 2), "nothing written yet")
	require.True(t, exceedsRatio(1, 0, 2), "any write without a denominator exceeds")
}

func TestArchiveProgressFailsClosedWithoutCompressedSize(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxRatio: 2, FilePath: filepath.Join(t.TempDir(), "missing.zip")}
	_, err := xFile.archiveProgress(0, 0, 0)
	require.ErrorIs(t, err, ErrMaxRatio)
}
