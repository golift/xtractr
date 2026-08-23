package xtractr

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCloser struct {
	err error
}

func (s stubCloser) Close() error {
	return s.err
}

func TestCloseNamed(t *testing.T) {
	t.Parallel()

	t.Run("records close error", func(t *testing.T) {
		t.Parallel()

		closeErr := errors.New("boom")

		var err error

		closeNamed(stubCloser{err: closeErr}, &err)
		require.ErrorIs(t, err, closeErr)
	})

	t.Run("keeps existing error", func(t *testing.T) {
		t.Parallel()

		first := errors.New("write failed")
		err := first

		closeNamed(stubCloser{err: errors.New("close failed")}, &err)
		assert.Equal(t, first, err)
	})

	t.Run("nil closer is a no-op", func(t *testing.T) {
		t.Parallel()

		var err error

		closeNamed(nil, &err)
		require.NoError(t, err)
	})
}

func TestCheckCRC32(t *testing.T) {
	t.Parallel()

	t.Run("skips when want is zero", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, checkCRC32("unused", 1, 0))
	})

	t.Run("accepts a match", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, checkCRC32("unused", 0xabcdef01, 0xabcdef01))
	})

	t.Run("removes dest on mismatch", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "corrupt.bin")
		require.NoError(t, os.WriteFile(path, []byte("nope"), 0o600))

		err := checkCRC32(path, 1, 2)
		require.ErrorIs(t, err, ErrChecksum)

		_, statErr := os.Stat(path)
		require.ErrorIs(t, statErr, os.ErrNotExist)
	})
}
