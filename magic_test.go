package xtractr_test

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

// signatureTestCase holds a magic byte sequence and a human-readable label.
type signatureTestCase struct {
	name  string
	magic []byte
}

func TestDetectBySignature(t *testing.T) {
	t.Parallel()

	cases := []signatureTestCase{
		{"ZIP", []byte{0x50, 0x4B, 0x03, 0x04}},
		{"RAR_v4", []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}},
		{"RAR_v5", []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}},
		{"7z", []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}},
		{"Gzip", []byte{0x1F, 0x8B}},
		{"Bzip2", []byte{0x42, 0x5A, 0x68}},
		{"XZ", []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}},
		{"Zstandard", []byte{0x28, 0xB5, 0x2F, 0xFD}},
		{"LZ4", []byte{0x04, 0x22, 0x4D, 0x18}},
		{"LZMA", []byte{0x5D, 0x00, 0x00}},
		{"Brotli", []byte{0xCE, 0xB2, 0xCF, 0x81}},
		{"AR_DEB", []byte{0x21, 0x3C, 0x61, 0x72, 0x63, 0x68, 0x3E, 0x0A}},
		{"RPM", []byte{0xED, 0xAB, 0xEE, 0xDB}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			filePath := filepath.Join(dir, "testfile.bin")

			// Write magic bytes followed by some padding.
			data := append(testCase.magic, bytes.Repeat([]byte{0x00}, 64)...) //nolint:gocritic
			require.NoError(t, os.WriteFile(filePath, data, 0o600))

			assert.True(t, xtractr.IsArchiveFileByContent(filePath),
				"IsArchiveFileByContent should detect %s signature", testCase.name)
		})
	}
}

func TestDetectBySignatureISO(t *testing.T) {
	t.Parallel()

	cd001 := []byte{0x43, 0x44, 0x30, 0x30, 0x31} // "CD001"
	offsets := []int{0x8001, 0x8801, 0x9001}

	for _, offset := range offsets {
		t.Run("offset_"+filepath.Base(string(rune(offset))), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			filePath := filepath.Join(dir, "test.bin")

			// Create a file large enough to place the signature at the given offset.
			data := make([]byte, offset+len(cd001)+16)
			copy(data[offset:], cd001)
			require.NoError(t, os.WriteFile(filePath, data, 0o600))

			assert.True(t, xtractr.IsArchiveFileByContent(filePath),
				"IsArchiveFileByContent should detect ISO9660 at offset 0x%X", offset)
		})
	}
}

func TestDetectBySignatureUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Random bytes that don't match any known signature.
	randomData := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE, 0x00, 0x11, 0x22, 0x33}

	filePath := filepath.Join(dir, "random.bin")
	require.NoError(t, os.WriteFile(filePath, randomData, 0o600))

	assert.False(t, xtractr.IsArchiveFileByContent(filePath),
		"IsArchiveFileByContent should return false for unknown bytes")
}

func TestIsArchiveFileByContent(t *testing.T) {
	t.Parallel()

	t.Run("valid_gzip", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		gzPath := filepath.Join(dir, "test.dat")

		// Create a real gzip file.
		var buf bytes.Buffer

		gw := gzip.NewWriter(&buf)
		_, err := gw.Write([]byte("hello world"))
		require.NoError(t, err)
		require.NoError(t, gw.Close())
		require.NoError(t, os.WriteFile(gzPath, buf.Bytes(), 0o600))

		assert.True(t, xtractr.IsArchiveFileByContent(gzPath))
	})

	t.Run("valid_zip", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		zipPath := filepath.Join(dir, "test.dat")

		// Create a real zip file.
		var buf bytes.Buffer

		zipWriter := zip.NewWriter(&buf)
		fileWriter, err := zipWriter.Create("file.txt")
		require.NoError(t, err)
		_, err = fileWriter.Write([]byte("content"))
		require.NoError(t, err)
		require.NoError(t, zipWriter.Close())
		require.NoError(t, os.WriteFile(zipPath, buf.Bytes(), 0o600))

		assert.True(t, xtractr.IsArchiveFileByContent(zipPath))
	})

	t.Run("plain_text_file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		txtPath := filepath.Join(dir, "readme.txt")
		require.NoError(t, os.WriteFile(txtPath, []byte("just a text file"), 0o600))

		assert.False(t, xtractr.IsArchiveFileByContent(txtPath))
	})

	t.Run("empty_file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		emptyPath := filepath.Join(dir, "empty")
		require.NoError(t, os.WriteFile(emptyPath, []byte{}, 0o600))

		assert.False(t, xtractr.IsArchiveFileByContent(emptyPath))
	})

	t.Run("nonexistent_file", func(t *testing.T) {
		t.Parallel()

		assert.False(t, xtractr.IsArchiveFileByContent("/nonexistent/path/file.bin"))
	})
}

// makeGzipData creates a valid gzip byte slice containing the given content.
func makeGzipData(t *testing.T, content string) []byte {
	t.Helper()

	var buf bytes.Buffer

	gzWriter := gzip.NewWriter(&buf)

	_, err := gzWriter.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, gzWriter.Close())

	return buf.Bytes()
}

// extractWithWrongExt writes archiveData to a file with the given filename,
// runs ExtractFile, and asserts the extraction succeeds via signature fallback.
func extractWithWrongExt(t *testing.T, filename string, archiveData []byte) {
	t.Helper()

	dir := t.TempDir()
	wrongPath := filepath.Join(dir, filename)

	require.NoError(t, os.WriteFile(wrongPath, archiveData, 0o600))

	outDir := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	size, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  wrongPath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err, "extraction should succeed via signature fallback")
	assert.Positive(t, size, "extracted data should have non-zero size")
	assert.NotEmpty(t, files, "should have extracted at least one file")
}

// TestExtractFileMismatchedExtension verifies the key use case from the issue:
// archives with wrong file extensions should still be extracted via signature fallback.
func TestExtractFileMismatchedExtension(t *testing.T) {
	t.Parallel()

	t.Run("gzip_with_rar_extension", func(t *testing.T) {
		t.Parallel()
		extractWithWrongExt(t, "archive.rar", makeGzipData(t, "extracted content"))
	})

	t.Run("zip_with_gz_extension", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer

		zipWriter := zip.NewWriter(&buf)

		fileWriter, err := zipWriter.Create("hello.txt")
		require.NoError(t, err)

		_, err = fileWriter.Write([]byte("zip content"))
		require.NoError(t, err)
		require.NoError(t, zipWriter.Close())

		extractWithWrongExt(t, "archive.gz", buf.Bytes())
	})

	t.Run("gzip_with_unknown_extension", func(t *testing.T) {
		t.Parallel()
		extractWithWrongExt(t, "archive.foobar", makeGzipData(t, "data in unknown ext"))
	})
}

// TestExtractFileExtensionStillWorks is a regression test verifying that normal
// extension-based extraction continues to work correctly after the signature
// detection feature was added.
func TestExtractFileExtensionStillWorks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a valid zip with the correct .zip extension.
	var buf bytes.Buffer

	zipWriter := zip.NewWriter(&buf)

	w, err := zipWriter.Create("readme.txt")
	require.NoError(t, err)

	_, err = w.Write([]byte("normal extraction"))
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())

	zipPath := filepath.Join(dir, "correct.zip")
	require.NoError(t, os.WriteFile(zipPath, buf.Bytes(), 0o600))

	outDir := filepath.Join(dir, "out")
	require.NoError(t, os.MkdirAll(outDir, 0o700))

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: outDir,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err, "extension-based extraction must still work")
	assert.Equal(t, uint64(len("normal extraction")), size)
	assert.Len(t, files, 1)
	assert.Len(t, archives, 1)

	// Verify extracted content is correct.
	extracted, err := os.ReadFile(filepath.Join(outDir, "readme.txt"))
	require.NoError(t, err)
	assert.Equal(t, "normal extraction", string(extracted))
}
