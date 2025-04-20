package xtractr_test

import (
	"archive/zip"
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestZip(t *testing.T) {
	t.Parallel()

	zip := makeZipFile(t)

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  zip.srcFilesDir,
		OutputDir: filepath.Clean(zip.dstFilesDir),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, zip.dataSize, size)
	assert.Len(t, files, zip.fileCount)
	assert.Len(t, archives, zip.archiveCount)
}

func makeZipFile(t *testing.T) testFilesInfo {
	t.Helper()

	const (
		dataSize     = uint64(21)
		fileCount    = 5
		archiveCount = 1
	)

	testFiles := []string{
		"README.txt",
		"subdir/",
		"subdir/subdirfile.txt",
		"subdir/level2/",
		"subdir/level2/level2file.txt",
	}

	name := t.TempDir()

	zipFile, err := os.Create(filepath.Join(name, "archive.zip"))
	require.NoError(t, err)
	defer safeCloser(t, zipFile)

	zipWriter := zip.NewWriter(zipFile)
	defer safeCloser(t, zipWriter)

	for _, file := range testFiles {
		if file[len(file)-1] == '/' {
			_, err = zipWriter.Create(file)
			require.NoError(t, err)
		} else {
			f, err := zipWriter.Create(file)
			require.NoError(t, err)
			_, err = f.Write([]byte("content"))
			require.NoError(t, err)
		}
	}

	return testFilesInfo{
		srcFilesDir:  filepath.Join(name, "archive.zip"),
		dstFilesDir:  name,
		dataSize:     dataSize,
		fileCount:    fileCount,
		archiveCount: archiveCount,
	}
}
