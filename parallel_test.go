package xtractr_test

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

const (
	parallelFileCount   = 20
	parallelWorkerCount = 4
	testContentSize     = 1024 // 1KB per file
	byteMask            = 256  // used to generate deterministic byte patterns
)

// createParallelTestZIP creates a ZIP archive with parallelFileCount files,
// each containing deterministic content. Returns the path to the archive.
func createParallelTestZIP(t *testing.T, directory string) string {
	t.Helper()

	zipPath := filepath.Join(directory, "parallel_test.zip")
	outFile, err := os.Create(zipPath)
	require.NoError(t, err)

	defer safeCloser(t, outFile)

	zipWriter := zip.NewWriter(outFile)
	defer safeCloser(t, zipWriter)

	// Create a subdirectory entry.
	_, err = zipWriter.Create("testdir/")
	require.NoError(t, err)

	for fileIdx := range parallelFileCount {
		fileName := fmt.Sprintf("testdir/file_%03d.txt", fileIdx)

		writer, writeErr := zipWriter.Create(fileName)
		require.NoError(t, writeErr)

		content := generateTestContent(fileIdx, testContentSize)
		_, writeErr = writer.Write(content)
		require.NoError(t, writeErr)
	}

	return zipPath
}

// generateTestContent creates deterministic content for a given file index.
func generateTestContent(fileIndex, size int) []byte {
	content := make([]byte, size)
	for byteIdx := range content {
		content[byteIdx] = byte((fileIndex + byteIdx) % byteMask)
	}

	return content
}

// verifyExtractedFiles checks that all extracted files exist with correct content.
func verifyExtractedFiles(t *testing.T, outputDir string) {
	t.Helper()

	for fileIdx := range parallelFileCount {
		fileName := fmt.Sprintf("testdir/file_%03d.txt", fileIdx)
		fullPath := filepath.Join(outputDir, fileName)

		data, err := os.ReadFile(fullPath)
		require.NoError(t, err, "file should exist: %s", fullPath)

		expected := generateTestContent(fileIdx, testContentSize)
		assert.Equal(t, expected, data, "file content mismatch: %s", fileName)
	}
}

func TestParallelZIPExtraction(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	zipPath := createParallelTestZIP(t, tmpDir)

	// Extract with FileWorkers > 1 (parallel).
	parallelOutDir := filepath.Join(tmpDir, "parallel_out")
	require.NoError(t, os.MkdirAll(parallelOutDir, 0o700))

	parallelSize, parallelFiles, parallelErr := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    zipPath,
		OutputDir:   parallelOutDir,
		FileMode:    0o600,
		DirMode:     0o700,
		FileWorkers: parallelWorkerCount,
	})
	require.NoError(t, parallelErr)

	// Extract with FileWorkers = 1 (sequential) for comparison.
	sequentialOutDir := filepath.Join(tmpDir, "sequential_out")
	require.NoError(t, os.MkdirAll(sequentialOutDir, 0o700))

	sequentialSize, sequentialFiles, sequentialErr := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: sequentialOutDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, sequentialErr)

	// Both should produce the same total bytes and file count.
	assert.Equal(t, sequentialSize, parallelSize, "parallel and sequential should write the same bytes")
	assert.Len(t, parallelFiles, len(sequentialFiles), "parallel and sequential should produce the same file count")

	// Verify content correctness for both.
	verifyExtractedFiles(t, parallelOutDir)
	verifyExtractedFiles(t, sequentialOutDir)
}

func TestProgressThreadSafety(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	zipPath := createParallelTestZIP(t, tmpDir)

	outDir := filepath.Join(tmpDir, "progress_out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	var callCount atomic.Int64

	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    zipPath,
		OutputDir:   outDir,
		FileMode:    0o600,
		DirMode:     0o700,
		FileWorkers: parallelWorkerCount,
		Progress: func(_ xtractr.Progress) {
			callCount.Add(1)
		},
	})
	require.NoError(t, err)
	assert.Positive(t, callCount.Load(), "progress callback should have been called")
}

func TestParallelZIPErrorPropagation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ZIP with a path traversal file.
	zipPath := filepath.Join(tmpDir, "malicious.zip")
	outFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(outFile)

	// Add a normal file first.
	normalWriter, err := zipWriter.Create("normal.txt")
	require.NoError(t, err)

	_, err = normalWriter.Write([]byte("safe"))
	require.NoError(t, err)

	// Add a path traversal file using raw header.
	header := &zip.FileHeader{
		Name:   "../../../etc/evil.txt",
		Method: zip.Store,
	}

	evilWriter, err := zipWriter.CreateHeader(header)
	require.NoError(t, err)

	_, err = evilWriter.Write([]byte("malicious"))
	require.NoError(t, err)

	require.NoError(t, zipWriter.Close())
	require.NoError(t, outFile.Close())

	outDir := filepath.Join(tmpDir, "evil_out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	_, _, extractErr := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    zipPath,
		OutputDir:   outDir,
		FileMode:    0o600,
		DirMode:     0o700,
		FileWorkers: parallelWorkerCount,
	})
	require.Error(t, extractErr)
	assert.ErrorIs(t, extractErr, xtractr.ErrInvalidPath)
}

func TestFileWorkersDefault(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	zipPath := createParallelTestZIP(t, tmpDir)

	// FileWorkers = 0 should behave like FileWorkers = 1 (sequential).
	outDirZero := filepath.Join(tmpDir, "zero_out")
	require.NoError(t, os.MkdirAll(outDirZero, 0o700))

	sizeZero, filesZero, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    zipPath,
		OutputDir:   outDirZero,
		FileMode:    0o600,
		DirMode:     0o700,
		FileWorkers: 0,
	})
	require.NoError(t, err)

	outDirOne := filepath.Join(tmpDir, "one_out")
	require.NoError(t, os.MkdirAll(outDirOne, 0o700))

	sizeOne, filesOne, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: outDirOne,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)

	assert.Equal(t, sizeOne, sizeZero, "FileWorkers=0 should produce same size as FileWorkers=1")
	assert.Len(t, filesZero, len(filesOne), "FileWorkers=0 should produce same file count")

	verifyExtractedFiles(t, outDirZero)
}

func TestProgressUpdatesChannel(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	zipPath := createParallelTestZIP(t, tmpDir)

	outDir := filepath.Join(tmpDir, "channel_out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	updates := make(chan xtractr.Progress, parallelFileCount*parallelWorkerCount)

	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:    zipPath,
		OutputDir:   outDir,
		FileMode:    0o600,
		DirMode:     0o700,
		FileWorkers: parallelWorkerCount,
		Updates:     updates,
	})
	require.NoError(t, err)

	// Drain the channel and verify we got progress updates including Done.
	var gotDone bool

	for len(updates) > 0 {
		prog := <-updates
		if prog.Done {
			gotDone = true
		}
	}

	assert.True(t, gotDone, "should have received a Done progress update")
}
