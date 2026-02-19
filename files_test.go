package xtractr_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
		"/path0/path1/path2/path3/file.r00",  // 10 because no file.rar.
		"/path0/path1/path2/path3/file2.r00", // skip because RAR v
		"/path0/path1/path2/path3/file2.RAR", // 11
		"/path0/path1/path2/path3/file3.iso", // 12
		"/path0/path1/path2/path3/file.nfo",  // not archive
	}

	const (
		dirMode  = 0o755
		fileMode = 0o644
	)

	base := t.TempDir()

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

func TestFindCompressedFilesSkipsDotFiles(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	// Valid archive files that should be found.
	require.NoError(t, os.WriteFile(filepath.Join(base, "file.rar"), []byte("content"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, "file.zip"), []byte("content"), 0o600))
	// Dot-prefixed files mimicking macOS AppleDouble metadata entries.
	// These caused Readdir to fail on NFS/SMB mounts in Docker (Unpackerr/unpackerr#541).
	require.NoError(t, os.WriteFile(filepath.Join(base, "._file.rar"), []byte("metadata"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(base, ".DS_Store"), []byte("metadata"), 0o600))

	paths := xtractr.FindCompressedFiles(xtractr.Filter{Path: base})
	assert.NotNil(t, paths, "archives should be found even when dot-prefixed files are present")
	assert.Equal(t, 2, paths.Count(), "only non-dot-prefixed archives should be returned")

	for _, file := range paths.List() {
		assert.NotEqual(t, '.', rune(filepath.Base(file)[0]), "dot-prefixed file should not be in results: "+file)
	}
}

func TestAllExcept(t *testing.T) {
	t.Parallel()

	includeOnlyThese := []string{".rar", ".zip", ".7z"}
	allExcept := xtractr.AllExcept(includeOnlyThese...)

	assert.Len(t, allExcept, len(xtractr.SupportedExtensions())-len(includeOnlyThese),
		"we should have 3 fewer entries that the total supported extensions")
}

func TestIsErrNameTooLong(t *testing.T) {
	t.Parallel()

	assert.False(t, xtractr.IsErrNameTooLong(nil))
	assert.False(t, xtractr.IsErrNameTooLong(errors.New("other error")))

	assert.True(t, xtractr.IsErrNameTooLong(syscall.ENAMETOOLONG))
	assert.True(t, xtractr.IsErrNameTooLong(errors.Join(errors.New("wrap"), syscall.ENAMETOOLONG)))
	assert.True(t, xtractr.IsErrNameTooLong(errors.New("open foo: file name too long")))
}

func TestTruncatePathForFS(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Short path is returned unchanged (and file doesn't exist).
	short := filepath.Join(dir, "short.docx")
	out, err := xtractr.TruncatePathForFS(short)
	require.NoError(t, err)
	assert.Equal(t, short, out)

	// Long basename is truncated to 255 bytes; extension preserved.
	longStem := strings.Repeat("a", 300)
	longPath := filepath.Join(dir, longStem+".docx")
	out, err = xtractr.TruncatePathForFS(longPath)
	require.NoError(t, err)
	assert.Equal(t, dir, filepath.Dir(out))
	base := filepath.Base(out)
	assert.LessOrEqual(t, len(base), 255)
	assert.Equal(t, ".docx", filepath.Ext(out))

	// When truncated name already exists, ~1 is used.
	require.NoError(t, os.WriteFile(out, []byte("x"), 0o600))

	out2, err := xtractr.TruncatePathForFS(longPath)
	require.NoError(t, err)
	assert.Contains(t, filepath.Base(out2), "~1")
	assert.Equal(t, ".docx", filepath.Ext(out2))

	// Stem consistency: when multiple conflicts exist (~1, ~2, ...), each candidate
	// must use the same base stem. Would have caught the "stem mutated in loop" bug.
	require.NoError(t, os.WriteFile(out2, []byte("x"), 0o600))

	out3, err := xtractr.TruncatePathForFS(longPath)
	require.NoError(t, err)

	base3 := filepath.Base(out3)
	assert.Contains(t, base3, "~2", "third call should return ~2 when truncated and ~1 exist")

	stem1 := strings.TrimSuffix(filepath.Base(out2), "~1.docx")
	stem2 := strings.TrimSuffix(base3, "~2.docx")
	assert.Equal(t, stem1, stem2, "stems for ~1 and ~2 must be identical (no mutation in loop)")
	assert.LessOrEqual(t, len(base3), 255)

	// Extension longer than nameMax: must not panic; truncateToBytes gets maxBytes <= 0
	// without the fix. Resulting path may still be long; we only assert no panic and no error.
	longExt := strings.Repeat("x", 300)
	pathLongExt := filepath.Join(dir, "a."+longExt)
	outLongExt, err := xtractr.TruncatePathForFS(pathLongExt)
	require.NoError(t, err)
	assert.NotEmpty(t, outLongExt)
	assert.Equal(t, dir, filepath.Dir(outLongExt))
}
