package xtractr_test

import (
	"archive/zip"
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golift.io/xtractr"
)

func TestZip(t *testing.T) {
	t.Parallel()

	zipFile := makeZipFile(t)

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  zipFile.srcFilesDir,
		OutputDir: filepath.Clean(zipFile.dstFilesDir),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, zipFile.dataSize, size)
	assert.Len(t, files, zipFile.fileCount)
	assert.Len(t, archives, zipFile.archiveCount)
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

// createNonUTF8ZipFile creates a zip archive containing files whose names are
// encoded in the specified charset. The NonUTF8 flag is set on each entry to
// simulate archives created by tools that use legacy encodings (e.g., WinRAR
// on a Chinese locale system).
func createNonUTF8ZipFile(t *testing.T, dir string, encodedNames [][]byte, content []byte) string {
	t.Helper()

	zipPath := filepath.Join(dir, "non_utf8.zip")

	outFile, err := os.Create(zipPath)
	require.NoError(t, err)

	defer safeCloser(t, outFile)

	zipWriter := zip.NewWriter(outFile)
	defer safeCloser(t, zipWriter)

	for _, rawName := range encodedNames {
		header := &zip.FileHeader{
			Name:    string(rawName),
			Method:  zip.Deflate,
			NonUTF8: true,
		}

		writer, err := zipWriter.CreateHeader(header)
		require.NoError(t, err)

		_, err = writer.Write(content)
		require.NoError(t, err)
	}

	return zipPath
}

//nolint:gosmopolitan
func TestZipNonUTF8_GB2312(t *testing.T) {
	t.Parallel()

	// Encode Chinese filenames in GBK (superset of GB2312).
	encoder := simplifiedchinese.GBK.NewEncoder()

	chineseNames := []string{"测试文件.txt", "数据目录/报告.txt"}
	encodedNames := make([][]byte, len(chineseNames))

	for idx, name := range chineseNames {
		encoded, err := encoder.Bytes([]byte(name))
		require.NoError(t, err)

		encodedNames[idx] = encoded
	}

	tmpDir := t.TempDir()
	content := []byte("hello")
	zipPath := createNonUTF8ZipFile(t, tmpDir, encodedNames, content)

	outDir := filepath.Join(tmpDir, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	size, files, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(len(content)*len(chineseNames)), size)
	assert.Len(t, files, len(chineseNames))

	// Verify extracted filenames are valid UTF-8 Chinese text.
	for idx, file := range files {
		base := filepath.Base(file)
		expected := filepath.Base(chineseNames[idx])
		assert.Equal(t, expected, base,
			"extracted filename should be properly decoded Chinese text")
	}

	// Verify the files actually exist on disk with the correct names.
	for _, name := range chineseNames {
		fullPath := filepath.Join(outDir, name)
		_, err := os.Stat(fullPath)
		assert.NoError(t, err, "decoded file should exist on disk: %s", fullPath)
	}
}

func TestZipNonUTF8_ShiftJIS(t *testing.T) {
	t.Parallel()

	// Encode Japanese filenames in Shift-JIS.
	encoder := japanese.ShiftJIS.NewEncoder()

	japaneseNames := []string{"\u30c6\u30b9\u30c8.txt", "\u30c7\u30fc\u30bf.txt"}
	encodedNames := make([][]byte, len(japaneseNames))

	for idx, name := range japaneseNames {
		encoded, err := encoder.Bytes([]byte(name))
		require.NoError(t, err)

		encodedNames[idx] = encoded
	}

	tmpDir := t.TempDir()
	content := []byte("hello")
	zipPath := createNonUTF8ZipFile(t, tmpDir, encodedNames, content)

	outDir := filepath.Join(tmpDir, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	size, files, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(len(content)*len(japaneseNames)), size)
	assert.Len(t, files, len(japaneseNames))

	// Verify extracted filenames are valid UTF-8 Japanese text.
	for idx, file := range files {
		base := filepath.Base(file)
		expected := filepath.Base(japaneseNames[idx])
		assert.Equal(t, expected, base,
			"extracted filename should be properly decoded Japanese text")
	}
}

//nolint:gosmopolitan
func TestZipUTF8FilenamesUnchanged(t *testing.T) {
	t.Parallel()

	// Ensure that archives with UTF-8 filenames (including CJK characters
	// that are already valid UTF-8) are not mangled by the detection logic.
	utf8Names := []string{"readme.txt", "日本語.txt", "中文.txt"}

	tmpDir := t.TempDir()

	zipPath := filepath.Join(tmpDir, "utf8.zip")

	outFile, err := os.Create(zipPath)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(outFile)

	content := []byte("data")

	for _, name := range utf8Names {
		// Default Create sets UTF-8 flag.
		writer, err := zipWriter.Create(name)
		require.NoError(t, err)

		_, err = writer.Write(content)
		require.NoError(t, err)
	}

	require.NoError(t, zipWriter.Close())
	require.NoError(t, outFile.Close())

	outDir := filepath.Join(tmpDir, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	_, files, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Len(t, files, len(utf8Names))

	for idx, file := range files {
		assert.Equal(t, utf8Names[idx], filepath.Base(file),
			"UTF-8 filenames must not be altered")
	}
}
