package xtractr

import (
	"fmt"
	"io"
	"os"
	"slices"
)

// sparseBlock is the zero run stored as a hole. zstd and xz use the same size.
// A shorter run is written out, so a small file of zeros stays dense.
const sparseBlock = 32 << 10

// sparseWriter stores runs of zeros as holes in a regular file.
// A hole at the end is closed with one zero byte at the last offset, which is
// what keeps the file size correct: a seek past the end does not extend it.
// If a seek fails, the zeros are written and this file stays dense after that.
// zeroHole is a range skipped instead of written. It is punched after the
// file has its final size: a punch before the next write is filled back in
// on APFS.
type zeroHole struct {
	start  int64
	length int64
}

type sparseWriter struct {
	out     io.WriteSeeker
	file    *os.File
	holes   []zeroHole
	pending int64
	zeros   [sparseBlock]byte
	disable bool
	dense   bool
	marked  bool
}

func newSparseWriter(file *os.File, disable bool) *sparseWriter {
	return &sparseWriter{out: file, file: file, disable: disable}
}

// Write accepts logical bytes. A run of sparseBlock zeros becomes a hole.
// Shorter runs stay in the same write as the surrounding data, and a trailing
// run is held so the next write can extend it. The count returned is the
// logical size, which is what the progress and byte caps see.
func (s *sparseWriter) Write(data []byte) (int, error) {
	if len(data) == 0 || s.disable || s.dense {
		return s.writeDense(data)
	}

	return s.writeRuns(data)
}

// writeRuns splits data into holes and dense writes. A zero run at the end of
// this call stays pending so the next call can extend it into a hole.
func (s *sparseWriter) writeRuns(data []byte) (int, error) {
	accepted := 0

	for len(data) > 0 {
		if data[0] == 0 {
			count := leadingZeros(data)
			s.pending += int64(count)
			accepted += count
			data = data[count:]

			continue
		}

		wrote, rest, err := s.writeNonZeros(data)
		accepted += wrote
		data = rest

		if err != nil {
			return accepted, err
		}
	}

	return accepted, nil
}

// writeNonZeros writes the pending short zero run and the dense prefix of data.
// A trailing zero run is left in the returned remainder.
func (s *sparseWriter) writeNonZeros(data []byte) (int, []byte, error) {
	err := s.flushHole()
	if err != nil {
		return 0, data, err
	}

	if s.dense {
		wrote, denseErr := s.writeDense(data)

		return wrote, data[wrote:], denseErr
	}

	err = s.writePending()
	if err != nil {
		return 0, data, err
	}

	count := denseSpan(data)
	if count == len(data) {
		count -= trailingZeros(data)
	}

	if count == 0 {
		return 0, data, fmt.Errorf("writing file: %w", io.ErrShortWrite)
	}

	wrote, err := s.writeDense(data[:count])

	return wrote, data[wrote:], err
}

// finish writes a short trailing zero run, or closes a trailing hole with one
// zero byte at the last offset.
func (s *sparseWriter) finish() error {
	var err error

	if !s.disable && !s.dense && s.pending >= sparseBlock {
		err = s.closeHole()
	} else {
		err = s.writePending()
	}

	if err != nil {
		return err
	}

	for _, hole := range s.holes {
		punchFileHole(s.file, hole.start, hole.length)
	}

	return nil
}

// flushHole seeks over whole sparse blocks and leaves a short remainder to be
// written with the data that follows. A failed seek writes the zeros instead.
func (s *sparseWriter) flushHole() error {
	if s.pending < sparseBlock {
		return nil
	}

	skip := s.pending - s.pending%sparseBlock

	err := s.seek(skip)
	if err != nil {
		s.dense = true

		return s.writePending()
	}

	s.pending -= skip

	return nil
}

func (s *sparseWriter) closeHole() error {
	err := s.seek(s.pending - 1)
	if err != nil {
		s.dense = true

		return s.writePending()
	}

	_, err = s.out.Write([]byte{0})
	if err != nil {
		return fmt.Errorf("closing hole: %w", err)
	}

	s.pending = 0

	return nil
}

func (s *sparseWriter) seek(skip int64) error {
	err := s.markSparse()
	if err != nil {
		return err
	}

	start, err := s.out.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("seeking over zeros: %w", err)
	}

	_, err = s.out.Seek(skip, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("seeking over zeros: %w", err)
	}

	s.holes = append(s.holes, zeroHole{start: start, length: skip})

	return nil
}

func (s *sparseWriter) markSparse() error {
	if s.marked || s.file == nil {
		s.marked = true

		return nil
	}

	err := markFileSparse(s.file)
	if err != nil {
		return err
	}

	s.marked = true

	return nil
}

func (s *sparseWriter) writeDense(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	count, err := s.out.Write(data)
	if err != nil {
		return count, fmt.Errorf("writing file: %w", err)
	}

	if count == 0 {
		return 0, fmt.Errorf("writing file: %w", io.ErrShortWrite)
	}

	return count, nil
}

func (s *sparseWriter) writePending() error {
	for s.pending > 0 {
		n := min(s.pending, sparseBlock)

		wrote, err := s.out.Write(s.zeros[:int(n)])
		s.pending -= int64(wrote)

		if err != nil {
			return fmt.Errorf("writing zeros: %w", err)
		}

		if wrote == 0 {
			return fmt.Errorf("writing zeros: %w", io.ErrShortWrite)
		}
	}

	return nil
}

func leadingZeros(data []byte) int {
	for idx, value := range data {
		if value != 0 {
			return idx
		}
	}

	return len(data)
}

func trailingZeros(data []byte) int {
	count := 0

	for _, d := range slices.Backward(data) {
		if d != 0 {
			break
		}

		count++
	}

	return count
}

// denseSpan is the prefix to write in one piece: everything before a zero run
// long enough to be a hole. data must start with a non-zero byte.
func denseSpan(data []byte) int {
	run := 0

	for idx, value := range data {
		if value != 0 {
			run = 0

			continue
		}

		run++

		if run == sparseBlock {
			return idx - sparseBlock + 1
		}
	}

	return len(data)
}
