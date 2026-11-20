package xtractr_test

import (
	"archive/tar"
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestDirectoryTimesSurviveMemberWrites(t *testing.T) {
	t.Parallel()

	dirTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	fileTime := time.Date(2024, 6, 7, 8, 9, 10, 0, time.UTC)

	t.Run("tar", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		archive := filepath.Join(dir, "dated.tar")
		writeDatedTar(t, archive, dirTime, fileTime)

		out := filepath.Join(dir, "out")
		_, _, err := xtractr.ExtractTar(&xtractr.XFile{
			FilePath:  archive,
			OutputDir: out,
			FileMode:  0o644,
			DirMode:   0o755,
		})
		require.NoError(t, err)
		assertTimes(t, out, dirTime, fileTime)
	})

	t.Run("zip", func(t *testing.T) {
		t.Parallel()
		extractDatedZip(t, 0, dirTime, fileTime)
	})

	t.Run("zip workers", func(t *testing.T) {
		t.Parallel()
		extractDatedZip(t, 4, dirTime, fileTime)
	})
}

func extractDatedZip(t *testing.T, workers int, dirTime, fileTime time.Time) {
	t.Helper()

	dir := t.TempDir()
	archive := filepath.Join(dir, "dated.zip")
	writeDatedZip(t, archive, dirTime, fileTime)

	out := filepath.Join(dir, "out")
	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    archive,
		OutputDir:   out,
		FileMode:    0o644,
		DirMode:     0o755,
		FileWorkers: workers,
	})
	require.NoError(t, err)
	assertTimes(t, out, dirTime, fileTime)
}

func assertTimes(t *testing.T, out string, dirTime, fileTime time.Time) {
	t.Helper()

	info, err := os.Stat(filepath.Join(out, "pkg"))
	require.NoError(t, err)
	assert.Equal(t, dirTime.Unix(), info.ModTime().UTC().Unix(), "directory time")

	info, err = os.Stat(filepath.Join(out, "pkg", "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, fileTime.Unix(), info.ModTime().UTC().Unix(), "file time")
}

func writeDatedTar(t *testing.T, dest string, dirTime, fileTime time.Time) {
	t.Helper()

	file, err := os.Create(dest)
	require.NoError(t, err)

	writer := tar.NewWriter(file)
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name:     "pkg/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  dirTime,
	}))

	body := []byte("hi")
	require.NoError(t, writer.WriteHeader(&tar.Header{
		Name:     "pkg/a.txt",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
		ModTime:  fileTime,
	}))
	_, err = writer.Write(body)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
}

func writeDatedZip(t *testing.T, dest string, dirTime, fileTime time.Time) {
	t.Helper()

	file, err := os.Create(dest)
	require.NoError(t, err)

	writer := zip.NewWriter(file)

	dirHeader := &zip.FileHeader{Name: "pkg/"}
	dirHeader.SetMode(os.ModeDir | 0o755)
	dirHeader.Modified = dirTime
	_, err = writer.CreateHeader(dirHeader)
	require.NoError(t, err)

	fileHeader := &zip.FileHeader{Name: "pkg/a.txt"}
	fileHeader.SetMode(0o644)
	fileHeader.Modified = fileTime
	entry, err := writer.CreateHeader(fileHeader)
	require.NoError(t, err)
	_, err = entry.Write([]byte("hi"))
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
}
