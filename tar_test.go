package xtractr_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	const (
		loremIpsum = `Lorem ipsum dolor sit amet, consectetur adipiscing elit, 
sed do eiusmod tempor incididunt ut labore et dolore magna aliqua.
Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris nisi ut aliquip
ex ea commodo consequat. Duis aute irure dolor in reprehenderit in voluptate velit 
esse cillum dolore eu fugiat nulla pariatur. Excepteur sint occaecat cupidatat non 
proident, sunt in culpa qui officia deserunt mollit anim id est laborum.`
		testDataSize     = 1544
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

	testDataDir, err := os.MkdirTemp(".", "xtractr_test_*_data")
	require.NoError(t, err, "creating temp directory failed")
	t.Cleanup(func() {
		os.RemoveAll(testDataDir)
	})

	sourceFilesDir := filepath.Join(testDataDir, "sources")
	err = os.MkdirAll(sourceFilesDir, 0o700)
	require.NoError(t, err)

	var destFilesDir string
	for _, file := range testFiles {
		fullPath := filepath.Join(sourceFilesDir, file)
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
		err = os.MkdirAll(destFilesDir, 0o700)
		require.NoError(t, err)
	}

	tests := []struct {
		name        string
		compressor  compressor
		extension   string
		skipWindows bool
	}{
		{"tar", &tarCompressor{}, "tar", false},
		{"tarZ", &tarZCompressor{}, "tar.z", true},
		{"tarBzip", &tarBzipCompressor{}, "tar.bz2", false},
		{"tarXZ", &tarXZCompressor{}, "tar.xz", false},
		{"tarGzip", &tarGzipCompressor{}, "tar.gz", false},
	}

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if runtime.GOOS == "windows" && test.skipWindows {
				t.Log("skipping test on windows")
			}

			archiveBase := filepath.Join(destFilesDir, "archive")
			err := test.compressor.Compress(t, sourceFilesDir, archiveBase)
			require.NoError(t, err)

			extractDir := filepath.Join(destFilesDir, test.name)
			err = os.Mkdir(extractDir, 0o700)
			require.NoError(t, err)

			size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
				FilePath:  archiveBase + "." + test.extension,
				OutputDir: filepath.Clean(extractDir),
				FileMode:  0o600,
				DirMode:   0o700,
			})
			require.NoError(t, err)
			assert.Equal(t, int64(testDataSize), size)
			assert.Len(t, files, testFileCount)
			assert.Len(t, archives, testArchiveCount)
		})
	}
}

func safeCloser(t *testing.T, c io.Closer) {
	t.Helper()
	err := c.Close()
	require.NoError(t, err)
}

func writeTar(t *testing.T, sourceDir string, destWriter io.Writer) error {
	t.Helper()

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
	require.NoError(t, outErr)

	return nil
}

func (c *tarCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarFile, err := os.Create(destBase + ".tar")
	defer safeCloser(t, tarFile)
	require.NoError(t, err)

	return writeTar(t, sourceDir, tarFile)
}

func (c *tarZCompressor) Compress(t *testing.T, sourceDir string, destBase string) error {
	t.Helper()
	tarFilename := destBase + ".tar"

	tarFile, err := os.Create(tarFilename)
	require.NoError(t, err)

	err = writeTar(t, sourceDir, tarFile)
	require.NoError(t, err)
	tarFile.Close()

	cmd := exec.Command("compress", tarFilename).Run()
	assert.NoError(t, cmd)

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

	err = writeTar(t, sourceDir, bzip2Writer)
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

	err = writeTar(t, sourceDir, xzWriter)
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

	err = writeTar(t, sourceDir, gzipWriter)
	require.NoError(t, err)

	return nil
}
