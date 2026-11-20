package xtractr_test

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Unpackerr/iso9660"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestTarSquashRootDotNamesKeepExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	require.NoError(t, os.Mkdir(out, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(out, "keep.txt"), []byte("stay"), 0o600))

	archive := filepath.Join(dir, "dot.tar")
	writePlainTar(t, archive, map[string]string{"./pkg/a.txt": "hi"})

	_, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(out, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "stay", string(got))

	for _, name := range files {
		assert.NotContains(t, filepath.Base(name), "keep.txt", "returned list: %v", files)
	}

	assert.FileExists(t, filepath.Join(out, "a.txt"))
}

func TestSquashRootOutputDirSpelling(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "one.zip")
	writeZip(t, archive, map[string][]byte{"pkg/a.txt": []byte("hi")})

	doubled := dir + string(filepath.Separator) + string(filepath.Separator) + "out"
	_, _, err := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  doubled,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "out", "a.txt"))
}

func TestSquashRootRelativeOutput(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	archive := filepath.Join(dir, "one.tar.gz")
	require.NoError(t, writeGzipTarMember(archive, "pkg/a.txt", []byte("hi")))

	extractSquashedTarGz(t, archive, "out")
	assert.FileExists(t, filepath.Join(dir, "out", "a.txt"))
}

func TestISOSquashRootOnce(t *testing.T) {
	t.Parallel()

	t.Run("nested chain squashes one level", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		archive := writeISO(t, dir, map[string]string{"dir/sub/file.txt": "hi"})
		out := filepath.Join(dir, "out")

		_, _, _, err := xtractr.ExtractFile(&xtractr.XFile{
			FilePath:   archive,
			OutputDir:  out,
			FileMode:   0o644,
			DirMode:    0o755,
			SquashRoot: true,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, fileDepth(t, out), "tree: %v", relTree(t, out))
	})

	t.Run("two top entries stay put", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		archive := writeISO(t, dir, map[string]string{
			"dir/file.txt": "hi",
			"readme.txt":   "bye",
		})
		out := filepath.Join(dir, "out")

		_, _, _, err := xtractr.ExtractFile(&xtractr.XFile{
			FilePath:   archive,
			OutputDir:  out,
			FileMode:   0o644,
			DirMode:    0o755,
			SquashRoot: true,
		})
		require.NoError(t, err)

		entries, err := os.ReadDir(out)
		require.NoError(t, err)

		dirs := 0

		for _, entry := range entries {
			if entry.IsDir() {
				dirs++
			}
		}

		assert.Equal(t, 1, dirs, "tree: %v", relTree(t, out))
	})
}

func TestCPIOSquashRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "one.cpio")
	require.NoError(t, writeCPIO(archive, []cpioEntry{
		{name: "pkg/a.txt", body: []byte("hi")},
	}))

	out := filepath.Join(dir, "out")
	_, _, err := xtractr.ExtractCPIO(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(out, "a.txt"))
	assert.NoFileExists(t, filepath.Join(out, "pkg"))
}

// test_data/udf-single-root.iso is one folder, pkg/a.txt. It was made with:
//
//	mkdir -p src/pkg && printf 'hi\n' > src/pkg/a.txt
//	hdiutil makehybrid -udf -iso -joliet -o udf-single-root.iso src
func TestUDFSquashRoot(t *testing.T) {
	t.Parallel()

	out := filepath.Join(t.TempDir(), "out")
	_, _, err := xtractr.ExtractISO(&xtractr.XFile{
		FilePath:   filepath.Join("test_data", "udf-single-root.iso"),
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(out, "a.txt"))
	assert.NoFileExists(t, filepath.Join(out, "pkg"))
}

func extractSquashedTarGz(t *testing.T, archive, out string) {
	t.Helper()

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)
}

func writePlainTar(t *testing.T, dest string, members map[string]string) {
	t.Helper()

	file, err := os.Create(dest)
	require.NoError(t, err)

	writer := tar.NewWriter(file)
	for name, body := range members {
		require.NoError(t, writer.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}))
		_, err = writer.Write([]byte(body))
		require.NoError(t, err)
	}

	require.NoError(t, writer.Close())
	require.NoError(t, file.Close())
}

func writeISO(t *testing.T, dir string, members map[string]string) string {
	t.Helper()

	writer, err := iso9660.NewWriter()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, writer.Cleanup())
	})

	for name, body := range members {
		require.NoError(t, writer.AddFile(bytes.NewReader([]byte(body)), name))
	}

	path := filepath.Join(dir, "disc.iso")
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, writer.WriteTo(file, "test"))
	require.NoError(t, file.Close())

	return path
}

func fileDepth(t *testing.T, root string) int {
	t.Helper()

	depth := -1

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)

		if entry.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		require.NoError(t, err)

		depth = strings.Count(rel, string(filepath.Separator))

		return nil
	})
	require.NoError(t, err)
	require.NotEqual(t, -1, depth, "no file under %s (%v)", root, relTree(t, root))

	return depth
}

func relTree(t *testing.T, root string) []string {
	t.Helper()

	var names []string

	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, _ error) error {
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}

		if entry.IsDir() {
			rel += "/"
		}

		names = append(names, rel)

		return nil
	})

	return names
}
