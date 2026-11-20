package xtractr

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSparseWriterHolesMatchLogicalBytes(t *testing.T) {
	t.Parallel()

	payload := bytes.Join([][]byte{
		[]byte("hello"),
		make([]byte, sparseBlock),
		[]byte("mid"),
		make([]byte, sparseBlock+100),
	}, nil)

	// The zero run is split across writes, the way a decompressor feeds a file.
	path := writeSparseChunks(t, false,
		payload[:5+sparseBlock/2],
		payload[5+sparseBlock/2:],
	)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	if alloc, ok := allocatedBytes(path); ok {
		assert.Less(t, alloc, int64(len(payload)/2), "allocated %d, logical %d", alloc, len(payload))
	}
}

func TestSparseWriterShortZeroRunStaysDense(t *testing.T) {
	t.Parallel()

	buf := &seekBuffer{}
	writer := &sparseWriter{out: buf}
	_, err := writer.Write(make([]byte, sparseBlock-1))
	require.NoError(t, err)
	require.NoError(t, writer.finish())

	assert.Zero(t, buf.seeks)
	assert.Equal(t, make([]byte, sparseBlock-1), buf.data)
}

func TestSparseWriterDisableWritesZeros(t *testing.T) {
	t.Parallel()

	payload := append([]byte("x"), make([]byte, sparseBlock*2)...)
	path := writeSparseChunks(t, true, payload)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	if alloc, ok := allocatedBytes(path); ok {
		assert.Greater(t, alloc, int64(len(payload)/2), "allocated %d, logical %d", alloc, len(payload))
	}
}

func TestSparseWriterSeekFailureWritesZeros(t *testing.T) {
	t.Parallel()

	payload := append(append([]byte("ab"), make([]byte, sparseBlock)...), 'c')
	buf := &seekBuffer{fail: true}
	writer := &sparseWriter{out: buf}

	_, err := writer.Write(payload[:2])
	require.NoError(t, err)
	_, err = writer.Write(payload[2:])
	require.NoError(t, err)
	require.NoError(t, writer.finish())

	assert.Equal(t, payload, buf.data)
	assert.NotZero(t, buf.seeks)
}

func TestSparseGzipRoundTrip(t *testing.T) {
	t.Parallel()

	payload := append([]byte("hello"), make([]byte, sparseBlock*2)...)
	payload = append(payload, []byte("bye")...)
	payload = append(payload, make([]byte, sparseBlock+50)...)

	src := gzipFile(t, payload)
	out := filepath.Join(t.TempDir(), "out")
	require.NoError(t, os.Mkdir(out, 0o700))

	_, files, err := ExtractGzip(&XFile{FilePath: src, OutputDir: out, FileMode: 0o600, DirMode: 0o700})
	require.NoError(t, err)
	require.Len(t, files, 1)

	got, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	if alloc, ok := allocatedBytes(files[0]); ok {
		assert.Less(t, alloc, int64(len(payload)/2), "allocated %d, logical %d", alloc, len(payload))
	}
}

func TestCopyMoveKeepsHoles(t *testing.T) {
	t.Parallel()

	payload := append([]byte("move"), make([]byte, sparseBlock*2)...)
	src := writeSparseChunks(t, false, payload)
	info, err := os.Lstat(src)
	require.NoError(t, err)

	dst := filepath.Join(filepath.Dir(src), "moved")
	require.NoError(t, copyMoveFile(src, dst, "_xtractr", info))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	_, err = os.Lstat(src)
	require.ErrorIs(t, err, os.ErrNotExist)

	if alloc, ok := allocatedBytes(dst); ok {
		assert.Less(t, alloc, int64(len(payload)/2), "allocated %d, logical %d", alloc, len(payload))
	}
}

func writeSparseChunks(t *testing.T, disable bool, chunks ...[]byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	require.NoError(t, err)

	writer := newSparseWriter(file, disable)
	for _, chunk := range chunks {
		_, err = writer.Write(chunk)
		require.NoError(t, err)
	}

	require.NoError(t, writer.finish())
	require.NoError(t, file.Close())

	return path
}

func gzipFile(t *testing.T, payload []byte) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "payload.bin.gz")
	file, err := os.Create(path)
	require.NoError(t, err)

	zipWriter := gzip.NewWriter(file)
	_, err = zipWriter.Write(payload)
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())
	require.NoError(t, file.Close())

	return path
}

// seekBuffer is an in-memory WriteSeeker. A seek past the end grows the buffer
// with zeros, which is what a hole reads back as.
type seekBuffer struct {
	data  []byte
	pos   int
	seeks int
	fail  bool
}

func (b *seekBuffer) Write(data []byte) (int, error) {
	end := b.pos + len(data)
	if end > len(b.data) {
		grown := make([]byte, end)
		copy(grown, b.data)
		b.data = grown
	}

	copy(b.data[b.pos:], data)
	b.pos += len(data)

	return len(data), nil
}

func (b *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	b.seeks++

	if b.fail {
		return 0, errors.New("seek failed")
	}

	next := b.pos

	switch whence {
	case io.SeekStart:
		next = int(offset)
	case io.SeekCurrent:
		next = b.pos + int(offset)
	case io.SeekEnd:
		next = len(b.data) + int(offset)
	}

	if next < 0 {
		return 0, errors.New("negative seek")
	}

	if next > len(b.data) {
		grown := make([]byte, next)
		copy(grown, b.data)
		b.data = grown
	}

	b.pos = next

	return int64(next), nil
}
