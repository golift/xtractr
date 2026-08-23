package xtractr_test

import (
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xzlib "github.com/ulikunitz/xz"
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

func TestExtractZstandardCorruptChecksum(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt.zst")
	payload := []byte("hello checksum")

	zstdFile, err := os.Create(src)
	require.NoError(t, err)

	writer, err := zstd.NewWriter(zstdFile)
	require.NoError(t, err)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, zstdFile.Close())

	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Greater(t, len(raw), 4)
	raw[len(raw)-1] ^= 0xff

	bad := filepath.Join(t.TempDir(), "payload.txt.zst")
	require.NoError(t, os.WriteFile(bad, raw, 0o600)) //nolint:gosec

	out := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	_, _, err = xtractr.ExtractZstandard(&xtractr.XFile{
		FilePath:  bad,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, zstd.ErrCRCMismatch)

	_, statErr := os.Stat(filepath.Join(out, "payload.txt"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestExtractXZCorruptChecksum(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "payload.txt.xz")
	payload := []byte("hello checksum")

	xzFile, err := os.Create(src)
	require.NoError(t, err)

	writer, err := xzlib.NewWriter(xzFile)
	require.NoError(t, err)
	_, err = writer.Write(payload)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, xzFile.Close())

	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.Greater(t, len(raw), 16)
	raw[len(raw)/2] ^= 0xff

	bad := filepath.Join(t.TempDir(), "payload.txt.xz")
	require.NoError(t, os.WriteFile(bad, raw, 0o600)) //nolint:gosec

	out := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	_, _, err = xtractr.ExtractXZ(&xtractr.XFile{
		FilePath:  bad,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.Error(t, err)

	_, statErr := os.Stat(filepath.Join(out, "payload.txt"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
