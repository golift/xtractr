package xtractr_test

// Shared utility functions/structs used in testing only.

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type testFilesInfo struct {
	srcFilesDir  string
	dstFilesDir  string
	dataSize     int64
	fileCount    int
	archiveCount int
}

// Create test files for the tests and returns information
// about the created files and directories.
func createTestFiles(t *testing.T) *testFilesInfo {
	t.Helper()

	const (
		loremIpsum = `Lorem ipsum dolor sit amet, consectetur adipiscing elit, 
sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.
Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip
ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit 
esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non 
proident, sunt in culpa qui officia deserunt mollit anim id est laborum.`
		testDataSize     = 1544 // equals the above * 3 and the randomDigits * 2.
		testFileCount    = 5
		testArchiveCount = 1
	)

	randomDigits := []uint8{
		157, 242, 143, 106, 163, 159, 194, 141, 32, 22, 249, 78,
		225, 206, 190, 199, 99, 146, 53, 149, 239, 179, 72, 2, 197, 196, 91, 81, 192,
		241, 69, 166, 213, 172, 111, 117, 210, 51, 136, 185, 130, 109, 139, 57, 150, 63,
		85, 86, 204, 10, 26, 1, 186, 234, 96, 187, 205, 138, 224, 77, 114, 226, 16, 222,
		151, 175, 200, 116, 36, 198, 173, 168, 230, 4, 18, 245, 31, 214, 158, 105, 171,
		123, 195, 137, 40, 93, 83, 215, 6, 118, 161, 223, 43, 167, 7, 3, 113, 148, 201,
		125,
	}

	testFiles := []string{
		"README.txt",
		"level1/",
		"level1/level1.txt",
		"level1/level1.bin",
		"level1/level2/",
		"level1/level2/level2.txt",
		"level1/level2/level2.bin",
	}

	testDataDir := t.TempDir()
	srcFilesDir := filepath.Join(testDataDir, "sources")
	require.NoError(t, os.MkdirAll(srcFilesDir, 0o700))

	var destFilesDir string

	for _, file := range testFiles {
		fullPath := filepath.Join(srcFilesDir, file)

		var err error

		switch {
		case file[len(file)-1] == '/':
			err = os.MkdirAll(fullPath, 0o700)
		case filepath.Ext(file) == ".txt":
			err = os.WriteFile(fullPath, []byte(loremIpsum), 0o600)
		default:
			err = os.WriteFile(fullPath, randomDigits, 0o600)
		}

		require.NoError(t, err)

		destFilesDir = filepath.Join(testDataDir, "out")
		require.NoError(t, os.MkdirAll(destFilesDir, 0o700))
	}

	return &testFilesInfo{
		srcFilesDir:  srcFilesDir,
		dstFilesDir:  destFilesDir,
		dataSize:     testDataSize,
		fileCount:    testFileCount,
		archiveCount: testArchiveCount,
	}
}

func safeCloser(t *testing.T, c io.Closer) {
	t.Helper()

	err := c.Close()
	require.NoError(t, err)
}
