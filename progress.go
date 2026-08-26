package xtractr

import (
	"fmt"
	"io"
	"os"
	"sync"
)

const maxPercent = 100

// Progress provides data about an in-progress file extraction and/or decompression.
type Progress struct {
	// Total uncompressed bytes in the archive.
	// This number is not available in all archive types, and may be 0.
	Total uint64
	// Compressed is the size of the archive file (compressed size).
	// It may equal the Total (uncompressed) for non-compressed archives, like tar.
	Compressed uint64
	// Wrote this many bytes to disk.
	Wrote uint64
	// This many compressed bytes have been read from the archive.
	Read uint64
	// Files (number of) written to disk.
	Files int
	// Count of files in archive.
	// This number is not available in all archive types, and may be 0.
	Count int
	// Done is set to true in the final progress update.
	Done bool
	// This is the input file. Do not modify the data.
	XFile *XFile
	send  func()
}

// progressTracker wraps Progress with a mutex for thread-safe concurrent access.
// The mutex is kept out of Progress so Progress remains safely copyable (for channels/callbacks).
type progressTracker struct {
	Progress

	mu        sync.Mutex
	headerErr error // optional fast-fail from claimed archive headers
}

// Percent returns the percent of bytes read or written.
func (p *Progress) Percent() (perc float64) {
	if p.Total > 0 {
		return float64(p.Wrote) / float64(p.Total) * maxPercent
	} else if p.Compressed > 0 {
		return float64(p.Read) / float64(p.Compressed) * maxPercent
	}

	return 0
}

// ArchiveProgress is a helper/example function you can use in your code to print extraction percentages.
// @every - Should be a number between 1 and 50 or so. This controls how often to print the percentage.
// The values 1, 2, 4, 5, 10, 20 and 25 work best.
// @reset - If set true, a `\r` is printed before each line, which will reset it on most terminals.
// @exit - If exit is true, then the for loop exit and the process returns when Progress.Done is true.
// Set `exit` true if you want a separate printer for each archive. A good reason is parallel extractions.
func ArchiveProgress(every float64, progress chan Progress, reset, exit bool) { //nolint:cyclop
	var (
		perc, last float64
		pre        string
		mod        = "%s%.0f%% "
	)

	const extra = 0.000000001

	if reset {
		pre = "\r\033[K"
	}

	if every < 1 {
		mod = "%s%.1f%% "
	}

	for prog := range progress {
		if prog.Done && exit {
			fmt.Println()
			return
		}

		if prog.Done {
			fmt.Println()

			last = 0 // reset for the next archive.

			continue
		}

		if perc = prog.Percent(); perc == maxPercent && last < maxPercent {
			last = maxPercent

			fmt.Printf(mod, pre, perc)
		}

		if last == 0 && perc == 0 || perc > last+every {
			last = perc + extra // we add extra so 0% only prints once.

			fmt.Printf(mod, pre, perc)
		}
	}
}

func (x *XFile) newProgress(total, compressed uint64, count int) *progressTracker {
	tracker := &progressTracker{}
	tracker.Total = total
	tracker.Compressed = compressed
	tracker.Count = count
	tracker.XFile = x
	tracker.send = func() {}
	x.prog = tracker

	if x.Progress != nil {
		tracker.send = func() {
			x.Progress(tracker.snapshot())
		}
	}

	if x.Updates != nil {
		tracker.send = func() {
			x.Updates <- tracker.snapshot()
		}
	}

	return tracker
}

// newArchiveProgress is newProgress with Compressed set to the archive file
// size (or the provided compressed size when non-zero, e.g. summed volumes)
// so MaxRatio can be enforced even when member headers omit packed sizes.
// Claimed uncompressed size and entry counts from headers are checked here;
// callers that pass the container size as Total for progress (tar, cpio) must
// use newProgress instead so MaxBytes is not compared to the on-disk archive.
func (x *XFile) newArchiveProgress(total, compressed uint64, count int) *progressTracker {
	if compressed == 0 {
		compressed = archiveFileSize(x.FilePath)
	}

	tracker := x.newProgress(total, compressed, count)
	tracker.headerErr = x.checkClaimedLimits(total, count, compressed)

	return tracker
}

// snapshot returns a copy of the Progress data, safe to send to callbacks/channels.
func (p *progressTracker) snapshot() Progress {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.Progress
}

// safeSend attempts to send a progress update without blocking.
// Parallel workers use this to avoid flooding the progress channel.
// Uses TryLock so concurrent workers skip updates rather than serialize on them.
func (p *progressTracker) safeSend() {
	if p.mu.TryLock() {
		p.mu.Unlock()
		p.send()
	}
}

// progressWrapper wraps several io interfaces so we can count the bytes read and written to those interfaces.
type progressWrapper struct {
	io.Writer
	io.Reader
	io.ReaderAt
	*progressTracker

	parallel bool
}

func (p *progressWrapper) Write(data []byte) (n int, err error) {
	want := uint64(len(data))

	p.mu.Lock()

	err = p.checkWriteLocked(want)
	if err != nil {
		p.mu.Unlock()
		return 0, err
	}

	p.Wrote += want
	p.mu.Unlock()

	size, err := p.Writer.Write(data)
	if uint64(size) != want {
		p.mu.Lock()
		p.Wrote -= want - uint64(size)
		p.mu.Unlock()
	}

	if p.parallel {
		p.safeSend()
	} else {
		p.send()
	}

	return size, err //nolint:wrapcheck
}

func (p *progressWrapper) Close() error {
	closer, ok := p.Writer.(io.Closer)
	if !ok {
		return nil
	}

	return closer.Close() //nolint:wrapcheck
}

func (p *progressWrapper) Read(data []byte) (n int, err error) {
	size, err := p.Reader.Read(data)

	p.mu.Lock()
	p.Progress.Read += uint64(size)
	p.mu.Unlock()

	if p.parallel {
		p.safeSend()
	} else {
		p.send()
	}

	return size, err //nolint:wrapcheck
}

func (p *progressWrapper) ReadAt(data []byte, off int64) (n int, err error) {
	size, err := p.ReaderAt.ReadAt(data, off)

	p.mu.Lock()
	p.Progress.Read += uint64(size)
	p.mu.Unlock()

	if p.parallel {
		p.safeSend()
	} else {
		p.send()
	}

	return size, err //nolint:wrapcheck
}

func (p *progressTracker) wrapWriter(writer io.Writer, parallel bool) (io.Writer, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	err := p.addFileLocked()
	if err != nil {
		return nil, err
	}

	return &progressWrapper{Writer: writer, progressTracker: p, parallel: parallel}, nil
}

func (p *progressTracker) addFileLocked() error {
	if p.headerErr != nil {
		return p.headerErr
	}

	xFile := p.XFile
	if xFile != nil && xFile.MaxFiles > 0 && p.Files >= xFile.MaxFiles {
		return ErrMaxFiles
	}

	p.Files++

	return nil
}

func (p *progressTracker) checkWriteLocked(add uint64) error {
	if p.headerErr != nil {
		return p.headerErr
	}

	xFile := p.XFile
	if xFile == nil {
		return nil
	}

	if xFile.MaxBytes > 0 && p.Wrote+add > xFile.MaxBytes {
		return ErrMaxBytes
	}

	if exceedsRatio(p.Wrote+add, p.Compressed, xFile.MaxRatio) {
		return ErrMaxRatio
	}

	return nil
}

// archiveFileSize returns the size of path on disk, or 0 if it cannot be stat'd.
func archiveFileSize(path string) uint64 {
	if path == "" {
		return 0
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() <= 0 {
		return 0
	}

	return uint64(info.Size())
}

func archiveFileSizes(paths ...string) uint64 {
	var total uint64

	for _, path := range paths {
		total += archiveFileSize(path)
	}

	return total
}

// checkClaimedLimits fails closed when archive headers claim more than the
// configured caps. Headers can understate, so this never replaces runtime
// checks in Write / addFileLocked.
func (x *XFile) checkClaimedLimits(claimedBytes uint64, claimedFiles int, compressed uint64) error {
	if x.MaxBytes > 0 && claimedBytes > x.MaxBytes {
		return ErrMaxBytes
	}

	if x.MaxFiles > 0 && claimedFiles > x.MaxFiles {
		return ErrMaxFiles
	}

	if claimedBytes > 0 && exceedsRatio(claimedBytes, compressed, x.MaxRatio) {
		return ErrMaxRatio
	}

	return nil
}

func exceedsRatio(wrote, compressed uint64, ratio float64) bool {
	if ratio <= 0 || compressed == 0 {
		return false
	}

	return float64(wrote) > float64(compressed)*ratio
}

func (x *XFile) extractWriter(writer io.Writer) (io.Writer, error) {
	return x.wrapExtractWriter(writer, false)
}

func (x *XFile) wrapExtractWriter(writer io.Writer, parallel bool) (io.Writer, error) {
	if x == nil || x.prog == nil {
		return writer, nil
	}

	return x.prog.wrapWriter(writer, parallel)
}

func (x *XFile) countExtracted() error {
	if x == nil || x.prog == nil {
		return nil
	}

	x.prog.mu.Lock()
	defer x.prog.mu.Unlock()

	return x.prog.addFileLocked()
}

func (x *XFile) accountBytes(add uint64) error {
	if x == nil || x.prog == nil {
		return nil
	}

	x.prog.mu.Lock()
	defer x.prog.mu.Unlock()

	err := x.prog.checkWriteLocked(add)
	if err != nil {
		return err
	}

	x.prog.Wrote += add

	return nil
}

func (p *progressTracker) reader(reader io.Reader) io.Reader {
	return &progressWrapper{Reader: reader, progressTracker: p}
}

func (p *progressTracker) readAter(reader io.ReaderAt) io.ReaderAt {
	return &progressWrapper{ReaderAt: reader, progressTracker: p}
}

func (p *progressTracker) done() {
	p.Done = true
	p.send()
}
