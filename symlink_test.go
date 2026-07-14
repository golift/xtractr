package xtractr_test

import (
	"archive/zip"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

// TestZipSymlinks ensures ZIP symlink members become real symlinks, not text stubs.
func TestZipSymlinks(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	archivePath := filepath.Join(tmp, "libs.zip")
	extractDir := filepath.Join(tmp, "out")

	require.NoError(t, createSymlinkZip(archivePath))

	_, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  archivePath,
		OutputDir: extractDir,
		FileMode:  0o755,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Len(t, files, 3)

	realFile := filepath.Join(extractDir, "libfoo.so.1.2.3")
	info, err := os.Lstat(realFile)
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink)
	assert.Positive(t, info.Size())

	for linkName, wantTarget := range map[string]string{
		"libfoo.so.1": "libfoo.so.1.2.3",
		"libfoo.so":   "libfoo.so.1",
	} {
		linkPath := filepath.Join(extractDir, linkName)
		info, err = os.Lstat(linkPath)
		require.NoError(t, err, linkName)
		require.NotZero(t, info.Mode()&os.ModeSymlink, "%s should be a symlink", linkName)

		got, err := os.Readlink(linkPath)
		require.NoError(t, err, linkName)
		assert.Equal(t, wantTarget, got, linkName)
	}
}

// TestZipSymlinkEscape rejects symlink targets that leave OutputDir.
func TestZipSymlinkEscape(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	cases := []struct {
		name     string
		linkName string
	}{
		{name: "absolute", linkName: filepath.Join(tmp, "outside")},
		{name: "relative", linkName: "../outside"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			archivePath := filepath.Join(tmp, testCase.name+".zip")
			extractDir := filepath.Join(tmp, testCase.name+"-out")
			require.NoError(t, createLinkOnlyZip(archivePath, "escape.link", testCase.linkName))

			_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
				FilePath:  archivePath,
				OutputDir: extractDir,
				FileMode:  0o755,
				DirMode:   0o755,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, xtractr.ErrInvalidPath)
		})
	}
}

// TestSevenZipSymlinks covers the shared writeFile ModeSymlink path for 7z.
// Uses a committed fixture because some CI 7z builds follow symlinks when
// creating archives even with -snl.
func TestSevenZipSymlinks(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	extractDir := filepath.Join(tmp, "out")
	_, files, _, err := xtractr.Extract7z(&xtractr.XFile{
		FilePath:  filepath.Join("test_data", "symlink.7z"),
		OutputDir: extractDir,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Len(t, files, 2)

	info, err := os.Lstat(filepath.Join(extractDir, "link.txt"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)

	got, err := os.Readlink(filepath.Join(extractDir, "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "target.txt", got)

	data, err := os.ReadFile(filepath.Join(extractDir, "target.txt"))
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data))
}

// TestRarSymlinks covers RAR5 unix symlinks (targets live in redirection records).
func TestRarSymlinks(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	extractDir := filepath.Join(tmp, "out")
	_, files, _, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join("test_data", "symlink.rar"),
		OutputDir: extractDir,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Len(t, files, 2)

	info, err := os.Lstat(filepath.Join(extractDir, "link.txt"))
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "RAR symlink should be restored as a symlink")

	got, err := os.Readlink(filepath.Join(extractDir, "link.txt"))
	require.NoError(t, err)
	assert.Equal(t, "target.txt", got)

	data, err := os.ReadFile(filepath.Join(extractDir, "target.txt"))
	require.NoError(t, err)
	assert.Equal(t, "payload", string(data))
}

// TestZipSymlinkTooLong rejects oversized symlink payloads.
func TestZipSymlinkTooLong(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	archivePath := filepath.Join(tmp, "huge-link.zip")
	extractDir := filepath.Join(tmp, "out")

	require.NoError(t, createLinkOnlyZip(archivePath, "huge.link", strings.Repeat("a", 8*1024+1)))

	_, _, err = xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  archivePath,
		OutputDir: extractDir,
		FileMode:  0o755,
		DirMode:   0o755,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, xtractr.ErrSymlinkTooLong)
}

func createSymlinkZip(dest string) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)
	defer zipWriter.Close()

	payload := []byte("shared-object-bytes")
	header := &zip.FileHeader{
		Name:     "libfoo.so.1.2.3",
		Method:   zip.Deflate,
		Modified: time.Now(),
	}
	header.SetMode(0o755)

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create regular header: %w", err)
	}

	_, err = writer.Write(payload)
	if err != nil {
		return fmt.Errorf("write payload: %w", err)
	}

	for name, target := range map[string]string{
		"libfoo.so.1": "libfoo.so.1.2.3",
		"libfoo.so":   "libfoo.so.1",
	} {
		err := writeZipSymlink(zipWriter, name, target)
		if err != nil {
			return err
		}
	}

	return nil
}

func createLinkOnlyZip(dest, name, target string) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create zip: %w", err)
	}
	defer archiveFile.Close()

	zipWriter := zip.NewWriter(archiveFile)
	defer zipWriter.Close()

	return writeZipSymlink(zipWriter, name, target)
}

func writeZipSymlink(zipWriter *zip.Writer, name, target string) error {
	header := &zip.FileHeader{
		Name:     name,
		Method:   zip.Deflate,
		Modified: time.Now(),
	}
	header.SetMode(0o755 | fs.ModeSymlink)

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create symlink header: %w", err)
	}

	_, err = writer.Write([]byte(target))
	if err != nil {
		return fmt.Errorf("write symlink target: %w", err)
	}

	return nil
}
