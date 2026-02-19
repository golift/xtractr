package xtractr

import (
	"fmt"
	"io"
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

	mu sync.Mutex
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
	size, err := p.Writer.Write(data)

	p.mu.Lock()
	p.Wrote += uint64(size)
	p.mu.Unlock()

	if p.parallel {
		p.safeSend()
	} else {
		p.send()
	}

	return size, err //nolint:wrapcheck
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

func (p *progressTracker) writer(writer io.Writer) io.Writer {
	p.mu.Lock()
	p.Files++
	p.mu.Unlock()

	return &progressWrapper{Writer: writer, progressTracker: p}
}

func (p *progressTracker) parallelWriter(writer io.Writer) io.Writer {
	p.mu.Lock()
	p.Files++
	p.mu.Unlock()

	return &progressWrapper{Writer: writer, progressTracker: p, parallel: true}
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
