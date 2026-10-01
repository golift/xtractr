package xtractr

import (
	"bytes"
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

func TestTighterBudgetUsesRatioOmit(t *testing.T) {
	t.Parallel()

	// Raw room is 10 (100-90). After omitting 80, ratio room is 90, so the
	// sibling with 50 bytes left is the tighter leftover.
	writer := &progressTracker{Progress: Progress{Wrote: 90, Compressed: 20}, ratioOmit: 80}
	sibling := &progressTracker{Progress: Progress{Wrote: 50, Compressed: 20}}

	got := tighterBudget([]*progressTracker{writer, sibling}, 0, 0, 5)
	require.Equal(t, sibling, got)
}

func TestOmitNotedCorrectsWriterWhenSiblingIsSelected(t *testing.T) {
	t.Parallel()

	inner := filepath.Join(t.TempDir(), "inner.zip")
	writer := newSharedBudget()
	writer.Compressed = 1000
	writer.Wrote = 1000
	writer.noteArchiveOutput(inner, 900)

	selected := newSharedBudget()
	selected.Compressed = 1000
	selected.Wrote = 100

	xFile := &XFile{
		FilePath:   inner,
		MaxRatio:   5,
		prog:       selected,
		ratioPeers: []*progressTracker{writer, selected},
	}

	// Writer raw (1000+4500)/1000 = 5.5. After the writer omits its own
	// note, (100+4500)/1000 = 4.6, and the selected sibling matches that.
	_, err := xFile.archiveProgress(4500, 50, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(900), writer.ratioOmit)
	require.Equal(t, uint64(0), selected.ratioOmit)
	require.NotContains(t, writer.archiveOut, filepath.Clean(inner))

	_, err = xFile.archiveProgress(4500, 50, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(900), writer.ratioOmit, "a retry must not omit the writer's note twice")
}

func TestArchiveProgressPeerRatioStillCaps(t *testing.T) {
	t.Parallel()

	inner := filepath.Join(t.TempDir(), "inner.zip")
	writer := newSharedBudget()
	writer.Compressed = 100
	writer.Wrote = 490
	writer.noteArchiveOutput(inner, 80)

	sibling := newSharedBudget()
	sibling.Compressed = 100
	sibling.Wrote = 450

	xFile := &XFile{
		FilePath:   inner,
		MaxRatio:   5,
		prog:       writer,
		ratioPeers: []*progressTracker{writer, sibling},
	}

	// Writer room after omit is 90. Sibling room is 50. 60 fits only the writer.
	_, err := xFile.archiveProgress(60, 10, 1)
	require.ErrorIs(t, err, ErrMaxRatio)
	require.Equal(t, uint64(80), writer.ratioOmit)

	_, err = xFile.archiveProgress(40, 10, 1)
	require.NoError(t, err)
}

func TestArchiveProgressPeerMaxBytesStillCaps(t *testing.T) {
	t.Parallel()

	writer := newSharedBudget()
	writer.Compressed = 1000
	writer.Wrote = 10

	sibling := newSharedBudget()
	sibling.Compressed = 1000
	sibling.Wrote = 100

	xFile := &XFile{
		FilePath:   filepath.Join(t.TempDir(), "inner.zip"),
		MaxBytes:   150,
		MaxRatio:   5,
		prog:       writer,
		ratioPeers: []*progressTracker{writer, sibling},
	}

	_, err := xFile.archiveProgress(80, 10, 1)
	require.ErrorIs(t, err, ErrMaxBytes)
}

func TestNoteArchiveOutputSkipsUnlimitedRatio(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxRatio: 0, prog: newSharedBudget()}
	xFile.noteArchiveOutput(filepath.Join(t.TempDir(), "inner.zip"), 10)
	require.Nil(t, xFile.prog.archiveOut)

	xFile.MaxRatio = 5
	xFile.noteArchiveOutput(filepath.Join(t.TempDir(), "inner.zip"), 10)
	require.Len(t, xFile.prog.archiveOut, 1)
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

func TestArchiveProgressOmitsNotedIntermediate(t *testing.T) {
	t.Parallel()

	inner := filepath.Join(t.TempDir(), "inner.zip")
	xFile := &XFile{FilePath: inner, MaxRatio: 5, prog: newSharedBudget()}
	xFile.prog.Compressed = 1000
	xFile.prog.Wrote = 1000
	mkv := filepath.Join(filepath.Dir(inner), "movie.mkv")
	xFile.prog.noteArchiveOutput(inner, 900)
	xFile.prog.noteArchiveOutput(mkv, 50)

	// Raw (1000+4500)/1000 = 5.5. After omitting the nested zip, (100+4500)/1000 = 4.6.
	_, err := xFile.archiveProgress(4500, 50, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(900), xFile.prog.ratioOmit)
	require.Equal(t, uint64(1000), xFile.prog.Wrote, "MaxBytes still sees the intermediate file")
	require.Equal(t, uint64(1000), xFile.prog.Compressed, "MaxRatio keeps the parent size")
	require.NotContains(t, xFile.prog.archiveOut, inner)
	require.Equal(t, uint64(50), xFile.prog.archiveOut[mkv])

	_, err = xFile.archiveProgress(4500, 50, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(900), xFile.prog.ratioOmit, "a retry must not omit the same file twice")

	xFile.MaxBytes = 1000
	_, err = xFile.archiveProgress(1, 50, 1)
	require.ErrorIs(t, err, ErrMaxBytes)
}

func TestArchiveProgressDoesNotOmitUnnotedArchive(t *testing.T) {
	t.Parallel()

	inner := filepath.Join(t.TempDir(), "inner.zip")
	writer := newSharedBudget()
	writer.noteArchiveOutput(inner, 900)

	other := &XFile{FilePath: inner, MaxRatio: 5, prog: newSharedBudget()}
	other.prog.Compressed = 1000
	other.prog.Wrote = 1000

	_, err := other.archiveProgress(4500, 50, 1)
	require.ErrorIs(t, err, ErrMaxRatio)
	require.Equal(t, uint64(0), other.prog.ratioOmit)
	require.Equal(t, uint64(0), writer.ratioOmit)
	require.Contains(t, writer.archiveOut, filepath.Clean(inner))
}

func TestOmitNotedVolumesOnce(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tracker := newSharedBudget()
	rar := filepath.Join(dir, "movie.rar")
	r00 := filepath.Join(dir, "movie.r00")
	r01 := filepath.Join(dir, "movie.r01")
	part := filepath.Join(dir, "movie.7z.002")
	mkv := filepath.Join(dir, "movie.mkv")

	tracker.noteArchiveOutput(rar, 100)
	tracker.noteArchiveOutput(r00, 200)
	tracker.noteArchiveOutput(r01, 300)
	tracker.noteArchiveOutput(part, 400)
	tracker.noteArchiveOutput(mkv, 999)
	require.Len(t, tracker.archiveOut, 5)

	tracker.omitNoted(rar, r00, r01, part)
	require.Equal(t, uint64(1000), tracker.ratioOmit)
	require.Equal(t, map[string]uint64{mkv: 999}, tracker.archiveOut)

	tracker.omitNoted(rar, r00, r01, part)
	require.Equal(t, uint64(1000), tracker.ratioOmit)

	plain := &progressTracker{}
	plain.noteArchiveOutput(rar, 100)
	require.Nil(t, plain.archiveOut)
}

func TestArchiveProgressFailsClosedWithoutCompressedSize(t *testing.T) {
	t.Parallel()

	xFile := &XFile{MaxRatio: 2, FilePath: filepath.Join(t.TempDir(), "missing.zip")}
	_, err := xFile.archiveProgress(0, 0, 0)
	require.ErrorIs(t, err, ErrMaxRatio)
}

func TestPercentCapsAt100(t *testing.T) {
	t.Parallel()

	read := Progress{Read: 150, Compressed: 100}
	require.InDelta(t, float64(maxPercent), read.Percent(), 0.01)

	wrote := Progress{Wrote: 150, Total: 100}
	require.InDelta(t, float64(maxPercent), wrote.Percent(), 0.01)
}

func TestCountingReadSeekerCountsReads(t *testing.T) {
	t.Parallel()

	xFile := &XFile{}
	xFile.newProgress(0, 10, 0)

	src := bytes.NewReader([]byte("abcdefghij"))
	reader := xFile.countingReadSeeker(src)

	buf := make([]byte, 4)
	_, err := io.ReadFull(reader, buf)
	require.NoError(t, err)
	require.Equal(t, uint64(4), xFile.prog.Read)

	_, err = reader.Seek(2, io.SeekCurrent)
	require.NoError(t, err)
	require.Equal(t, uint64(4), xFile.prog.Read)

	_, err = io.ReadFull(reader, buf)
	require.NoError(t, err)
	require.Equal(t, uint64(8), xFile.prog.Read)
}
