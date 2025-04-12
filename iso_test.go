package xtractr_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kdomanski/iso9660"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestIso(t *testing.T) {
	t.Parallel()

	testFilesInfo := createTestFiles(t)
	writer, err := iso9660.NewWriter()
	require.NoError(t, err, "failed to create writer")
	defer func() {
		err = writer.Cleanup()
		require.NoError(t, err, "failed to cleanup writer")
	}()

	size := int64(0)
	walkErr := filepath.Walk(testFilesInfo.srcFilesDir, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err, "unexpected")

		if info.IsDir() {
			return nil
		}

		fileToAdd, err := os.Open(path)
		require.NoError(t, err, "failed to open file")
		defer fileToAdd.Close()

		fStat, err := fileToAdd.Stat()
		require.NoError(t, err, "failed to stat file")
		size += fStat.Size()

		err = writer.AddFile(fileToAdd, strings.TrimPrefix(fileToAdd.Name(), testFilesInfo.srcFilesDir))
		require.NoError(t, err, "failed to add file")

		return nil
	})
	require.NoError(t, walkErr, "failed to walk files")

	isoFileName := filepath.Join(testFilesInfo.dstFilesDir, "archive.iso")
	isoFile, err := os.Create(isoFileName)
	defer safeCloser(t, isoFile)
	require.NoError(t, err, "failed to create ISO file")

	err = writer.WriteTo(isoFile, "test")
	require.NoError(t, err, "failed to write ISO")

	xSize, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  isoFileName,
		OutputDir: filepath.Clean(testFilesInfo.dstFilesDir),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, testFilesInfo.dataSize, size, "data written does not match predetermined size")
	assert.Equal(t, testFilesInfo.dataSize, xSize, "data extracted does not match predetermined size")
	assert.Len(t, files, testFilesInfo.fileCount)
	assert.Len(t, archives, testFilesInfo.archiveCount)
}
