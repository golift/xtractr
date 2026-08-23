package xtractr_test

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestExtractGzipCorruptChecksum(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt.gz")
	payload := []byte("hello checksum")

	gzipFile, err := os.Create(src)
	require.NoError(t, err)

	writer := gzip.NewWriter(gzipFile)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, gzipFile.Close())

	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Greater(t, len(raw), 8)
	raw[len(raw)-5] ^= 0xff

	bad := filepath.Join(t.TempDir(), "payload.txt.gz")
	require.NoError(t, os.WriteFile(bad, raw, 0o600)) //nolint:gosec

	out := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	_, _, err = xtractr.ExtractGzip(&xtractr.XFile{
		FilePath:  bad,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(out, "payload.txt"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExtractGzip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt.gz")
	payload := []byte("hello checksum")

	gzipFile, err := os.Create(src)
	require.NoError(t, err)

	writer := gzip.NewWriter(gzipFile)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, gzipFile.Close())

	out := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	size, files, err := xtractr.ExtractGzip(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(len(payload)), size)
	require.Len(t, files, 1)

	got, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}
