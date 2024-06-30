package xtractr

import (
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed test_data/archive.zip
var zipData []byte

func TestZip(t *testing.T) {
	t.Parallel()
	const (
		testDataSize  = 35
		testFileCount = 3
	)

	name, err := os.MkdirTemp(".", "xtractr_test_*_data")
	require.NoError(t, err, "creating temp directory failed")
	defer os.RemoveAll(name)

	zipTestFile := filepath.Join(name, "archive.zip")
	err = os.WriteFile(zipTestFile, zipData, 0o600)
	require.NoError(t, err)

	size, files, err := ExtractZIP(&XFile{
		FilePath:  zipTestFile,
		OutputDir: filepath.Clean(name),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, int64(testDataSize), size)
	assert.Len(t, files, testFileCount)
}
