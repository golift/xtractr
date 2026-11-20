package xtractr

import (
	"bytes"
	"fmt"
	"io"
	"os"
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

// Write accepts logical bytes. Zeros are held until a non-zero byte or finish,
// so a run split across writes is still one hole. The count returned is the
// logical size, which is what the progress and byte caps see.
func (s *sparseWriter) Write(data []byte) (int, error) {
	if len(data) == 0 || s.disable || s.dense {
		return s.writeDense(data)
	}

	accepted := 0

	for len(data) > 0 {
		if data[0] == 0 {
			count := leadingZeros(data)
			s.pending += int64(count)
			accepted += count
			data = data[count:]

			continue
		}

		err := s.commitZeros(false)
		if err != nil {
			return accepted, err
		}

		if s.dense {
			count, err := s.writeDense(data)

			return accepted + count, err
		}

		count := leadingData(data)

		wrote, err := s.out.Write(data[:count])
		accepted += wrote
		data = data[wrote:]

		if err != nil {
			return accepted, fmt.Errorf("writing file: %w", err)
		}
	}

	return accepted, nil
}

// finish writes a short trailing zero run, or closes a trailing hole with one
// zero byte at the last offset.
func (s *sparseWriter) finish() error {
	err := s.commitZeros(true)
	if err != nil {
		return err
	}

	for _, hole := range s.holes {
		punchFileHole(s.file, hole.start, hole.length)
	}

	return nil
}

func (s *sparseWriter) commitZeros(atEnd bool) error {
	if s.pending == 0 {
		return nil
	}

	if s.dense || s.disable || s.pending < sparseBlock {
		return s.writePending()
	}

	if atEnd {
		return s.closeHole()
	}

	skip := s.pending - s.pending%sparseBlock

	err := s.seek(skip)
	if err != nil {
		s.dense = true

		return s.writePending()
	}

	s.pending -= skip

	return s.writePending()
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

func leadingData(data []byte) int {
	idx := bytes.IndexByte(data, 0)
	if idx < 0 {
		return len(data)
	}

	return idx
}
