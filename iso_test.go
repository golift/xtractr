package xtractr_test

import (
	"fmt"
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

	walkErr := filepath.Walk(testFilesInfo.srcFilesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("unexpected error: %w", err)
		}
		if info.IsDir() {
			return nil
		}

		fileToAdd, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open file: %w", err)
		}
		defer fileToAdd.Close()

		err = writer.AddFile(fileToAdd, strings.TrimLeft(fileToAdd.Name(), testFilesInfo.srcFilesDir))
		if err != nil {
			return fmt.Errorf("failed to add file: %w", err)
		}
		return nil
	})
	require.NoError(t, walkErr, "failed to walk files")

	isoFileName := filepath.Join(testFilesInfo.dstFilesDir, "archive.iso")
	isoFile, err := os.Create(isoFileName)
	defer safeCloser(t, isoFile)
	require.NoError(t, err, "failed to create ISO file")

	err = writer.WriteTo(isoFile, "test")
	require.NoError(t, err, "failed to write ISO")

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  isoFileName,
		OutputDir: filepath.Clean(testFilesInfo.dstFilesDir),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(testFilesInfo.dataSize), size)
	assert.Len(t, files, testFilesInfo.fileCount)
	assert.Len(t, archives, testFilesInfo.archiveCount)
}
