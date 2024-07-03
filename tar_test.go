package xtractr_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsnet/compress/bzip2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"
	"golift.io/xtractr"
)

type compressor interface {
	Compress(t *testing.T, sourceDir string, destFileBase string) error
}

type (
	tarCompressor     struct{}
	tarZCompressor    struct{}
	tarBzipCompressor struct{}
	tarXZCompressor   struct{}
	tarGzipCompressor struct{}
)

func TestTar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressor compressor
		extension  string
	}{
		{"tar", &tarCompressor{}, "tar"},
		{"tarZ", &tarZCompressor{}, "tar.z"},
		{"tarBzip", &tarBzipCompressor{}, "tar.bz2"},
		{"tarXZ", &tarXZCompressor{}, "tar.xz"},
		{"tarGzip", &tarGzipCompressor{}, "tar.gz"},
	}

	testFilesInfo := createTestFiles(t)
	require.NotNil(t, testFilesInfo)

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archiveBase := filepath.Join(testFilesInfo.dstFilesDir, "archive")
			err := test.compressor.Compress(t, testFilesInfo.srcFilesDir, archiveBase)
			require.NoError(t, err)

			extractDir := filepath.Join(testFilesInfo.dstFilesDir, test.name)
			err = os.Mkdir(extractDir, 0o700)
			require.NoError(t, err)

			size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
				FilePath:  archiveBase + "." + test.extension,
				OutputDir: filepath.Clean(extractDir),
				FileMode:  0o600,
				DirMode:   0o700,
			})
			require.NoError(t, err)
			assert.Equal(t, int64(testFilesInfo.dataSize), size)
			assert.Len(t, files, testFilesInfo.fileCount)
			assert.Len(t, archives, testFilesInfo.archiveCount)
		})
	}
}

func safeCloser(t *testing.T, c io.Closer) {
	t.Helper()
	err := c.Close()
	require.NoError(t, err)
}

func writeTar(sourceDir string, destWriter io.Writer) error {
	tarWriter := tar.NewWriter(destWriter)
	outErr := filepath.Walk(sourceDir, func(path string, info os.FileInfo, _ error) error {
		if info.Mode().IsDir() {
			return nil
		}
		relativePath := path[len(sourceDir):]
		if len(relativePath) == 0 {
			return nil
		}
		fileReader, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open file: %w", err)
		}
		defer fileReader.Close()

		h, err := tar.FileInfoHeader(info, relativePath)
		if err != nil {
			return fmt.Errorf("failed to create tar header: %w", err)
		}
		h.Name = relativePath
		if err = tarWriter.WriteHeader(h); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}
		if _, err := io.Copy(tarWriter, fileReader); err != nil {
			return fmt.Errorf("failed to copy file (%s) into tar header: %w", fileReader.Name(), err)
		}
		return nil
	})

	if outErr == nil {
		return nil
	}
	return fmt.Errorf("failed to walk source directory: %w", outErr)
}

func (c *tarCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarFile, err := os.Create(destBase + ".tar")
	defer safeCloser(t, tarFile)
	require.NoError(t, err)

	return writeTar(sourceDir, tarFile)
}

func (c *tarZCompressor) Compress(t *testing.T, _ string, destBase string) error {
	t.Helper()

	// No native Go library for .tar.Z and compress Unix utility is not available on
	// Windows. So, we use a pre-compressed file from test_data.
	tarZFilename := destBase + ".tar.z"
	tarZDestFile, err := os.Create(tarZFilename)
	require.NoError(t, err)

	tarZTestFile, err := os.Open("test_data/archive.tar.Z")
	require.NoError(t, err)

	written, err := io.Copy(tarZDestFile, tarZTestFile)
	assert.Greater(t, written, int64(0))
	require.NoError(t, err)

	return nil
}

func (c *tarBzipCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarBZ2Filename := destBase + ".tar.bz2"

	tarBZ2File, err := os.Create(tarBZ2Filename)
	require.NoError(t, err)

	bzip2Writer, err := bzip2.NewWriter(tarBZ2File, &bzip2.WriterConfig{Level: bzip2.BestSpeed})
	defer safeCloser(t, bzip2Writer)
	require.NoError(t, err)

	err = writeTar(sourceDir, bzip2Writer)
	require.NoError(t, err)

	return nil
}

func (c *tarXZCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarXZFilename := destBase + ".tar.xz"

	tarXZFile, err := os.Create(tarXZFilename)
	require.NoError(t, err)

	xzWriter, err := xz.NewWriter(tarXZFile)
	defer safeCloser(t, xzWriter)
	require.NoError(t, err)

	err = writeTar(sourceDir, xzWriter)
	require.NoError(t, err)

	return nil
}

func (c *tarGzipCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarGZFilename := destBase + ".tar.gz"

	tarGZFile, err := os.Create(tarGZFilename)
	require.NoError(t, err)

	gzipWriter := gzip.NewWriter(tarGZFile)
	defer gzipWriter.Close()
	require.NoError(t, err)

	err = writeTar(sourceDir, gzipWriter)
	require.NoError(t, err)

	return nil
}
