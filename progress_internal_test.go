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

func TestNewProgressSharedKeepsCompressedAndCounts(t *testing.T) {
	t.Parallel()

	xFile := &XFile{FilePath: "child.zip"}
	xFile.prog = newSharedBudget()
	xFile.prog.Compressed = 99
	xFile.prog.Wrote = 7
	xFile.prog.Files = 2

	xFile.newProgress(50, 12345, 3)
	require.Equal(t, uint64(99), xFile.prog.Compressed)
	require.Equal(t, uint64(7), xFile.prog.Wrote)
	require.Equal(t, 2, xFile.prog.Files)
	require.Equal(t, uint64(50), xFile.prog.Total)
	require.Equal(t, 3, xFile.prog.Count)
}

func TestCheckClaimedLimitsAddsExistingSharedCounts(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxFiles: 5, MaxBytes: 100, MaxRatio: 2}
	xFile.prog = newSharedBudget()
	xFile.prog.Files = 4
	xFile.prog.Wrote = 90

	require.ErrorIs(t, xFile.checkClaimedLimits(0, 2, 10), ErrMaxFiles)
	require.ErrorIs(t, xFile.checkClaimedLimits(20, 0, 10), ErrMaxBytes)
	require.ErrorIs(t, xFile.checkClaimedLimits(1, 0, 10), ErrMaxRatio)
}

func TestTighterBudgetPicksSmallerRemainingBytes(t *testing.T) {
	t.Parallel()

	loose := &progressTracker{Progress: Progress{Wrote: 10, Compressed: 100}}
	tight := &progressTracker{Progress: Progress{Wrote: 80, Compressed: 100}}

	got := tighterBudget([]*progressTracker{loose, tight}, 100, 0, 0)
	require.Equal(t, tight, got)
}

func TestTighterBudgetPicksSmallerRemainingFilesWhenBytesTie(t *testing.T) {
	t.Parallel()

	loose := &progressTracker{Progress: Progress{Wrote: 10, Files: 1}}
	tight := &progressTracker{Progress: Progress{Wrote: 10, Files: 8}}

	got := tighterBudget([]*progressTracker{loose, tight}, 100, 10, 0)
	require.Equal(t, tight, got)
}

func TestTighterBudgetPicksSmallerRatioRoom(t *testing.T) {
	t.Parallel()

	// Same wrote; smaller Compressed leaves less MaxRatio room.
	loose := &progressTracker{Progress: Progress{Wrote: 10, Compressed: 100}}
	tight := &progressTracker{Progress: Progress{Wrote: 10, Compressed: 20}}

	got := tighterBudget([]*progressTracker{loose, tight}, 0, 0, 2)
	require.Equal(t, tight, got)
}

func TestSharedSnapshotIsPerArchive(t *testing.T) {
	t.Parallel()

	xFile := &XFile{FilePath: "child.zip"}
	xFile.prog = newSharedBudget()
	xFile.prog.Compressed = 99
	xFile.prog.Wrote = 700
	xFile.prog.Files = 5

	xFile.newProgress(50, 20, 3)
	xFile.prog.Wrote += 10
	xFile.prog.Files++
	xFile.prog.Read = 4

	snap := xFile.prog.snapshot()
	require.Equal(t, uint64(10), snap.Wrote)
	require.Equal(t, 1, snap.Files)
	require.Equal(t, uint64(20), snap.Compressed)
	require.Equal(t, uint64(50), snap.Total)
	require.InDelta(t, 20, snap.Percent(), 0.01)

	require.Equal(t, uint64(710), xFile.prog.Wrote, "cap Wrote stays cumulative")
	require.Equal(t, 6, xFile.prog.Files)
	require.Equal(t, uint64(99), xFile.prog.Compressed, "MaxRatio keeps parent size")
}

func TestNewProgressSharedFillsCompressedOnce(t *testing.T) {
	t.Parallel()

	xFile := &XFile{FilePath: "parent.zip"}
	xFile.prog = newSharedBudget()
	xFile.newProgress(50, 40, 1)
	require.Equal(t, uint64(40), xFile.prog.Compressed)

	xFile.newProgress(10, 999, 1)
	require.Equal(t, uint64(40), xFile.prog.Compressed)
}

func TestArchiveProgressFailsClosedWithoutCompressedSize(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxRatio: 2, FilePath: filepath.Join(t.TempDir(), "missing.zip")}
	_, err := xFile.archiveProgress(0, 0, 0)
	require.ErrorIs(t, err, ErrMaxRatio)
}
