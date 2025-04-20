package xtractr

import (
	"fmt"
	"io"
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
// @exit - If exit is true, then the for loop exit and the process returns when Progress.Done is true.
// Set `exit` true if you want a separate printer for each archive. A good reason is parallel extractions.
func ArchiveProgress(every float64, progress chan Progress, exit bool) {
	var perc, last float64

	const extra = 0.000000001

	for prog := range progress {
		if prog.Done && exit {
			return
		}

		if prog.Done {
			last = 0 // reset for the next archive.
			continue
		}

		if perc = prog.Percent(); perc == maxPercent && last < maxPercent {
			fmt.Printf("%.00f%%\n", perc)

			last = maxPercent
		}

		if last == 0 && perc == 0 || perc > last+every {
			fmt.Printf("%.00f%% ", perc)
			last = perc + extra // we add extra so 0% only prints once.
		}
	}
}

func (x *XFile) newProgress(total, compressed uint64, count int) *Progress {
	x.prog = &Progress{Total: total, Compressed: compressed, Count: count, send: func() {}, XFile: x}

	if x.Progress != nil {
		x.prog.send = func() { x.Progress(*x.prog) }
	}

	if x.Updates != nil {
		x.prog.send = func() { x.Updates <- *x.prog }
	}

	return x.prog
}

// progressWrapper wraps several io interfaces so we can count the bytes read and written to those interfaces.
type progressWrapper struct {
	io.Writer
	io.Reader
	io.ReaderAt
	*Progress
}

func (p *progressWrapper) Write(data []byte) (n int, err error) {
	defer p.send()

	size, err := p.Writer.Write(data)
	p.Wrote += uint64(size)

	return size, err //nolint:wrapcheck
}

func (p *progressWrapper) Read(data []byte) (n int, err error) {
	defer p.send()

	size, err := p.Reader.Read(data)
	p.Progress.Read += uint64(size)

	return size, err //nolint:wrapcheck
}

func (p *progressWrapper) ReadAt(data []byte, off int64) (n int, err error) {
	defer p.send()

	size, err := p.ReaderAt.ReadAt(data, off)
	p.Progress.Read += uint64(size)

	return size, err //nolint:wrapcheck
}

func (p *Progress) writer(writer io.Writer) io.Writer {
	p.Files++
	return &progressWrapper{Writer: writer, Progress: p}
}

func (p *Progress) reader(reader io.Reader) io.Reader {
	return &progressWrapper{Reader: reader, Progress: p}
}

func (p *Progress) readAter(reader io.ReaderAt) io.ReaderAt {
	return &progressWrapper{ReaderAt: reader, Progress: p}
}

func (p *Progress) done() {
	p.Done = true
	p.send()
}
