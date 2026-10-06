package xtractr_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cavaliergopher/cpio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

// TestExtractCPIOSymlinkWithinOutput restores a relative link that climbs to a
// sibling inside the output directory.
func TestExtractCPIOSymlinkWithinOutput(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	tmp := t.TempDir()
	out := filepath.Join(tmp, "out")
	archive := filepath.Join(tmp, "within.cpio")

	require.NoError(t, writeCPIO(archive, []cpioEntry{
		{name: "real.txt", body: []byte("ok")},
		{name: "nested/link", symlink: true, target: "../real.txt"},
	}))

	err := extractCPIO(archive, out)
	require.NoError(t, err)

	got, err := os.Readlink(filepath.Join(out, "nested", "link"))
	require.NoError(t, err)
	assert.Equal(t, "../real.txt", got)
	assertPathInside(t, out, filepath.Join(out, "nested", "link"))

	body, err := os.ReadFile(filepath.Join(out, "real.txt"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(body))
}

// TestExtractCPIOSymlinkEscape rejects link targets that leave OutputDir.
// The "clean bypass" cases are the filepath.Clean hole: an in-archive symlink
// to ".." makes a later target look inside OutputDir after lexical cleaning
// while the kernel resolves it outside.
func TestExtractCPIOSymlinkEscape(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	t.Run("absolute", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		victim := filepath.Join(root, "victim")
		require.NoError(t, os.MkdirAll(victim, 0o750))

		archive := filepath.Join(root, "absolute.cpio")
		require.NoError(t, writeCPIO(archive, []cpioEntry{
			{name: "foo", symlink: true, target: victim},
			{name: "foo/authorized_keys", body: []byte("pwned")},
		}))

		err := extractCPIO(archive, out)
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "foo"))
		assertAbsent(t, filepath.Join(victim, "authorized_keys"))
	})

	t.Run("relative", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		archive := filepath.Join(root, "relative.cpio")
		require.NoError(t, writeCPIO(archive, []cpioEntry{
			{name: "escape", symlink: true, target: "../victim"},
		}))

		err := extractCPIO(archive, out)
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "escape"))
		assertAbsent(t, filepath.Join(root, "victim"))
	})

	t.Run("clean bypass", func(t *testing.T) {
		t.Parallel()
		assertCPIOCleanBypassRejected(t, true)
	})

	t.Run("clean bypass dangling", func(t *testing.T) {
		t.Parallel()
		assertCPIOCleanBypassRejected(t, false)
	})

	t.Run("cycle", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		archive := filepath.Join(root, "cycle.cpio")
		require.NoError(t, writeCPIO(archive, []cpioEntry{
			{name: "loop", symlink: true, target: "loop"},
			{name: "via", symlink: true, target: "loop"},
		}))

		err := extractCPIO(archive, out)
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "via"))
	})
}

func assertCPIOCleanBypassRejected(t *testing.T, victimExists bool) {
	t.Helper()

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	if victimExists {
		require.NoError(t, os.MkdirAll(victim, 0o750))
	}

	archive := filepath.Join(root, "bypass.cpio")
	require.NoError(t, writeCPIO(archive, []cpioEntry{
		{name: "foo/bar", symlink: true, target: ".."},
		{name: "baz", symlink: true, target: "foo/bar/../victim"},
		{name: "baz/pwned", body: []byte("pwned")},
	}))

	err := extractCPIO(archive, out)
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)

	got, err := os.Readlink(filepath.Join(out, "foo", "bar"))
	require.NoError(t, err)
	assert.Equal(t, "..", got)
	assertPathInside(t, out, filepath.Join(out, "foo", "bar"))

	assertAbsent(t, filepath.Join(out, "baz"))
	assertAbsent(t, filepath.Join(victim, "pwned"))

	if !victimExists {
		assertAbsent(t, victim)
	}
}

func assertPathInside(t *testing.T, base, target string) {
	t.Helper()

	resolvedBase, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)

	resolvedTarget, err := filepath.EvalSymlinks(target)
	require.NoError(t, err)

	rel, err := filepath.Rel(resolvedBase, resolvedTarget)
	require.NoError(t, err)

	escaped := rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
	assert.False(t, escaped, "resolved %s -> %s", resolvedTarget, rel)
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()

	_, err := os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func extractCPIO(archive, out string) error {
	_, _, err := xtractr.ExtractCPIO(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	if err != nil {
		return fmt.Errorf("extract cpio: %w", err)
	}

	return nil
}

type cpioEntry struct {
	name    string
	body    []byte
	target  string
	symlink bool
}

func writeCPIO(dest string, entries []cpioEntry) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create cpio: %w", err)
	}

	writer := cpio.NewWriter(archiveFile)

	for _, entry := range entries {
		err = writeCPIOEntry(writer, entry)
		if err != nil {
			_ = writer.Close()
			_ = archiveFile.Close()

			return err
		}
	}

	err = writer.Close()
	if err != nil {
		_ = archiveFile.Close()

		return fmt.Errorf("close cpio: %w", err)
	}

	err = archiveFile.Close()
	if err != nil {
		return fmt.Errorf("close cpio file: %w", err)
	}

	return nil
}

func writeCPIOEntry(writer *cpio.Writer, entry cpioEntry) error {
	payload := entry.body
	mode := cpio.FileMode(cpio.TypeReg | 0o644)

	if entry.symlink {
		payload = []byte(entry.target)
		mode = cpio.TypeSymlink | 0o777
	}

	err := writer.WriteHeader(&cpio.Header{
		Name:  entry.name,
		Mode:  mode,
		Size:  int64(len(payload)),
		Links: 1,
	})
	if err != nil {
		return fmt.Errorf("cpio header %s: %w", entry.name, err)
	}

	if len(payload) == 0 {
		return nil
	}

	_, err = writer.Write(payload)
	if err != nil {
		return fmt.Errorf("cpio write %s: %w", entry.name, err)
	}

	return nil
}

func skipWithoutSymlinks(t *testing.T) {
	t.Helper()

	err := os.Symlink("target", filepath.Join(t.TempDir(), "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
}
