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
	const (
		testDataSize     = 21
		testFileCount    = 5
		testArchiveCount = 1
	)
	testFiles := []string{
		"README.txt",
		"subdir/",
		"subdir/subdirfile.txt",
		"subdir/level2/",
		"subdir/level2/level2file.txt",
	}

	name := t.TempDir()
	defer os.RemoveAll(name)

	zipFile, err := os.Create(filepath.Join(name, "archive.zip"))
	require.NoError(t, err)
	zipWriter := zip.NewWriter(zipFile)

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
	err = zipWriter.Close()
	require.NoError(t, err)
	err = zipFile.Close()
	require.NoError(t, err)

	zipTestFile := filepath.Join(name, "archive.zip")

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  zipTestFile,
		OutputDir: filepath.Clean(name),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(testDataSize), size)
	assert.Len(t, files, testFileCount)
	assert.Len(t, archives, testArchiveCount)
}
