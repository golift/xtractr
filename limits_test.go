package xtractr_test

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

const (
	limitPayloadBytes = 4096
	limitZipFiles     = 4
)

func TestExtractGzipMaxBytes(t *testing.T) {
	t.Parallel()

	src, out := makeGzipOfZeros(t)

	_, _, err := xtractr.ExtractGzip(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxBytes:  100,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxBytes)
}

func TestExtractGzipMaxRatio(t *testing.T) {
	t.Parallel()

	src, out := makeGzipOfZeros(t)

	info, err := os.Stat(src)
	require.NoError(t, err)
	require.Greater(t, float64(limitPayloadBytes), float64(info.Size())*2)

	_, _, err = xtractr.ExtractGzip(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxRatio:  2,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxRatio)
}

func TestExtractZipMaxFiles(t *testing.T) {
	t.Parallel()

	src, out := makeEmptyFilesZip(t, limitZipFiles)

	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxFiles:  limitZipFiles - 1,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxFiles)
}

func TestExtractZipHeaderMaxFilesExistingDirs(t *testing.T) {
	t.Parallel()

	src, out := makeEmptyDirsZip(t, limitZipFiles)
	for idx := range limitZipFiles {
		require.NoError(t, os.MkdirAll(filepath.Join(out, fmt.Sprintf("dir_%d", idx)), 0o700))
	}

	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxFiles:  limitZipFiles - 1,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxFiles)
}

func TestExtractTarMaxFilesRuntime(t *testing.T) {
	t.Parallel()

	src, out := makeEmptyFilesTar(t, limitZipFiles)

	_, _, err := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxFiles:  limitZipFiles - 1,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxFiles)
}

func TestExtractTarEmptyMaxBytes(t *testing.T) {
	t.Parallel()

	src, out := makeEmptyFilesTar(t, 0)

	size, files, err := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxBytes:  1,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), size)
	require.Empty(t, files)
}

func TestExtractTarNestedDirsMaxFiles(t *testing.T) {
	t.Parallel()

	src, out := makeNestedFileTar(t, "a/b/c.txt")

	_, _, err := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
		MaxFiles:  2,
	})
	require.ErrorIs(t, err, xtractr.ErrMaxFiles)

	size, files, err := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  src,
		OutputDir: filepath.Join(out, "ok"),
		FileMode:  0o600,
		DirMode:   0o700,
		MaxFiles:  3,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(0), size)
	require.Len(t, files, 1)
}

func TestExtractGzipUnlimited(t *testing.T) {
	t.Parallel()

	src, out := makeGzipOfZeros(t)

	size, files, err := xtractr.ExtractGzip(&xtractr.XFile{
		FilePath:  src,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(limitPayloadBytes), size)
	require.Len(t, files, 1)
}

func TestQueueInheritsConfigMaxBytes(t *testing.T) {
	t.Parallel()

	src, _ := makeGzipOfZeros(t)
	dir := filepath.Dir(src)

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		MaxBytes: 100,
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:           xtractr.Filter{Path: dir},
		TempFolder:       true,
		DisableRecursion: true,
		CBChannel:        make(chan *xtractr.Response, 2),
	}

	_, err := queue.Extract(job)
	require.NoError(t, err)
	require.ErrorIs(t, waitExtract(t, job.CBChannel).Error, xtractr.ErrMaxBytes)
}

func TestQueueJobMaxBytesOverridesConfig(t *testing.T) {
	t.Parallel()

	src, _ := makeGzipOfZeros(t)
	dir := filepath.Dir(src)

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		MaxBytes: 100,
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:           xtractr.Filter{Path: dir},
		TempFolder:       true,
		DisableRecursion: true,
		MaxBytes:         limitPayloadBytes,
		CBChannel:        make(chan *xtractr.Response, 2),
	}

	_, err := queue.Extract(job)
	require.NoError(t, err)
	require.NoError(t, waitExtract(t, job.CBChannel).Error)
}

func waitExtract(t *testing.T, responses chan *xtractr.Response) *xtractr.Response {
	t.Helper()

	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case resp, ok := <-responses:
			require.True(t, ok, "callback channel closed before extraction completed")

			if resp.Done {
				return resp
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for queued extraction")
		}
	}
}

func makeGzipOfZeros(t *testing.T) (src, out string) {
	t.Helper()

	dir := t.TempDir()
	src = filepath.Join(dir, "payload.bin.gz")
	out = filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	gzipFile, err := os.Create(src)
	require.NoError(t, err)

	writer := gzip.NewWriter(gzipFile)
	_, err = writer.Write(make([]byte, limitPayloadBytes))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, gzipFile.Close())

	return src, out
}

func makeEmptyFilesZip(t *testing.T, count int) (src, out string) {
	t.Helper()

	dir := t.TempDir()
	src = filepath.Join(dir, "empty.zip")
	out = filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	zipFile, err := os.Create(src)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	for idx := range count {
		_, err = zipWriter.CreateHeader(&zip.FileHeader{
			Name:   fmt.Sprintf("file_%d.txt", idx),
			Method: zip.Store,
		})
		require.NoError(t, err)
	}

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	return src, out
}

func makeEmptyDirsZip(t *testing.T, count int) (src, out string) {
	t.Helper()

	dir := t.TempDir()
	src = filepath.Join(dir, "dirs.zip")
	out = filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	zipFile, err := os.Create(src)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(zipFile)
	for idx := range count {
		_, err = zipWriter.CreateHeader(&zip.FileHeader{
			Name:   fmt.Sprintf("dir_%d/", idx),
			Method: zip.Store,
		})
		require.NoError(t, err)
	}

	require.NoError(t, zipWriter.Close())
	require.NoError(t, zipFile.Close())

	return src, out
}

func makeEmptyFilesTar(t *testing.T, count int) (src, out string) {
	t.Helper()

	dir := t.TempDir()
	src = filepath.Join(dir, "empty.tar")
	out = filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	tarFile, err := os.Create(src)
	require.NoError(t, err)

	tarWriter := tar.NewWriter(tarFile)
	for idx := range count {
		err = tarWriter.WriteHeader(&tar.Header{
			Name: fmt.Sprintf("file_%d.txt", idx),
			Mode: 0o600,
			Size: 0,
		})
		require.NoError(t, err)
	}

	require.NoError(t, tarWriter.Close())
	require.NoError(t, tarFile.Close())

	return src, out
}

func makeNestedFileTar(t *testing.T, name string) (src, out string) {
	t.Helper()

	dir := t.TempDir()
	src = filepath.Join(dir, "nested.tar")
	out = filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(out, 0o700))

	tarFile, err := os.Create(src)
	require.NoError(t, err)

	tarWriter := tar.NewWriter(tarFile)
	require.NoError(t, tarWriter.WriteHeader(&tar.Header{
		Name: name,
		Mode: 0o600,
		Size: 0,
	}))
	require.NoError(t, tarWriter.Close())
	require.NoError(t, tarFile.Close())

	return src, out
}
