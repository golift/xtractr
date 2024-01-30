package xtractr_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func createTestPaths(t *testing.T) string {
	t.Helper()

	// testFileList currently has 12 files that should be considered archives.
	testFileList := []string{
		"/path0/file.iso",                    // 1
		"/path0/file.gz",                     // 2
		"/path0/file.rar",                    // 3
		"/path0/file.r00",                    // skip because rar ^
		"/path0/path1/file.iso",              // 4
		"/path0/path1/file.gz",               // 5
		"/path0/path1/file.r00",              // 6 because no rar
		"/path0/path1/path2/file.iso",        // 7
		"/path0/path1/path2/file.gz",         // 8
		"/path0/path1/path2/file.zip",        // 9
		"/path0/path1/path2/file.txt",        // not archive
		"/path0/path1/path2/path3/file.rar",  // 10
		"/path0/path1/path2/path3/file2.rar", // 11
		"/path0/path1/path2/path3/file3.iso", // 12
		"/path0/path1/path2/path3/file.nfo",  // not archive
	}

	const (
		dirMode  = 0o755
		fileMode = 0o644
	)

	base, err := os.MkdirTemp("", "GoTest_*_Path")
	require.NoError(t, err, "cannot create temp directory for testing")

	for _, testPath := range testFileList {
		testPath = filepath.Join(base, testPath)
		baseDir := filepath.Dir(testPath)
		require.NoError(t, os.MkdirAll(baseDir, dirMode), "cannot create temp directory for testing: "+baseDir)
		require.NoError(t, os.WriteFile(testPath, []byte("content"), fileMode),
			"cannot create temp file for testing: "+testPath)
	}

	return base
}

func TestFindCompressedFiles(t *testing.T) {
	t.Parallel()

	base := createTestPaths(t)
	defer os.RemoveAll(base)

	// Test 1
	total := 0
	paths := xtractr.FindCompressedFiles(xtractr.Filter{Path: base})
	for folder, files := range paths {
		total += len(files)
		// We purposely put 3 archives in each sub folder in the test paths.
		assert.Len(t, files, 3, "Wrong count of compressed items was located in: "+folder)
	}

	assert.Equal(t, 12, total, "Wrong total count of compressed items was located.")

	// Test 2
	paths = xtractr.FindCompressedFiles(xtractr.Filter{Path: base, MaxDepth: 1})
	total = 0

	for folder, files := range paths {
		total += len(files)
		// We purposely put 3 archives in each sub folder in the test paths.
		assert.Len(t, files, 3, "Wrong count of compressed items was located in: "+folder)
	}

	assert.Equal(t, 3, total, "With a max depth of 1, only the 3 files in the /path0 folder shall be returned.")

	// Test 3
	paths = xtractr.FindCompressedFiles(xtractr.Filter{Path: base, MinDepth: 3, MaxDepth: 3})
	total = 0

	for folder, files := range paths {
		total += len(files)
		// We purposely put 3 archives in each sub folder in the test paths.
		assert.Len(t, files, 3, "Wrong count of compressed items was located in: "+folder)
	}

	assert.Equal(t, 3, total, "With equal min and max depths, only 3 files in 1 directory shall be returned.")

	// Test 4
	paths = xtractr.FindCompressedFiles(xtractr.Filter{Path: base, MinDepth: 2})
	total = 0

	for folder, files := range paths {
		total += len(files)
		assert.Len(t, files, 3, "Wrong count of compressed items was located in: "+folder)
	}

	assert.Equal(t, 9, total, "With a min depth of 2, we skip the root folder and count the other 9 archives.")

	// Test 5
	paths = xtractr.FindCompressedFiles(xtractr.Filter{Path: base, ExcludeSuffix: []string{".iso"}})
	total = 0

	for folder, files := range paths {
		total += len(files)
		// We purposely put only 2 non-ISO archives in each test sub directory.
		assert.Len(t, files, 2, "Wrong count of compressed items was located in: "+folder)
	}

	assert.Equal(t, 8, total, "When skipping the four ISOs, we have 8 archives remaining.")
}
