package xtractr_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwaples/rardecode/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestCheckVolumes(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("rar4-volume-"), 20)

	t.Run("whole rar4", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 16}, rar4File{name: "payload.bin", data: payload})
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, "show.rar")})
	})

	t.Run("whole rar4 r00", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.r00", rar4Options{volSize: 10, firstVolume: true},
			rar4File{name: "payload.bin", data: payload})
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, "show.r00")})
	})

	t.Run("whole rar5", func(t *testing.T) {
		t.Parallel()

		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join("test_data", "multivol.part1.rar")})
	})

	t.Run("whole 7z", func(t *testing.T) {
		t.Parallel()

		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join("test_data", "multivol.7z.001")})
	})

	t.Run("whole 7z later volume", func(t *testing.T) {
		t.Parallel()

		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join("test_data", "multivol.7z.002")})
	})

	t.Run("continued file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 16}, rar4File{name: "payload.bin", data: payload})
		require.NoError(t, os.Remove(filepath.Join(dir, names[0])))
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, names[1])}, xtractr.ErrMissingVolume)
	})

	t.Run("second file starts at r00", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: len(payload)},
			rar4File{name: "first.bin", data: payload},
			rar4File{name: "second.bin", data: payload},
		)
		require.NoError(t, os.Remove(filepath.Join(dir, names[0])))
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, names[1])}, xtractr.ErrMissingVolume)
	})

	t.Run("no end block", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 16, noEnd: true},
			rar4File{name: "payload.bin", data: payload})
		require.NoError(t, os.Remove(filepath.Join(dir, names[len(names)-1])))
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, names[0])}, xtractr.ErrMissingVolume)
	})

	t.Run("rar5 volume number", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		for idx, src := range []string{"multivol.part2.rar", "multivol.part3.rar", "multivol.part4.rar"} {
			data, err := os.ReadFile(filepath.Join("test_data", src))
			require.NoError(t, err)

			dest := filepath.Join(dir, fmt.Sprintf("multivol.r%02d", idx))
			require.NoError(t, os.WriteFile(dest, data, 0o600)) //nolint:gosec // fixed name in a temp dir.
		}

		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, "multivol.r00")},
			xtractr.ErrMissingVolume, rardecode.ErrBadVolumeNumber)
	})

	t.Run("continued rar without first flag", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.rar", rar4Options{}, rar4File{name: "payload.bin", data: payload, continued: true})
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, "show.rar")},
			xtractr.ErrMissingVolume, rardecode.ErrInvalidFileBlock)
	})

	t.Run("lone r01", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		piece := []byte("0123456789")
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: len(piece)},
			rar4File{name: "payload.bin", data: bytes.Repeat(piece, 3)},
		)
		require.GreaterOrEqual(t, len(names), 3)
		require.NoError(t, os.Remove(filepath.Join(dir, names[0])))
		require.NoError(t, os.Remove(filepath.Join(dir, names[1])))
		assertChecked(t, &xtractr.XFile{FilePath: filepath.Join(dir, names[2])}, xtractr.ErrMissingVolume)
	})

	t.Run("short 7z", func(t *testing.T) {
		t.Parallel()

		data, err := os.ReadFile(filepath.Join("test_data", "multivol.7z.001"))
		require.NoError(t, err)

		dir := t.TempDir()
		path := filepath.Join(dir, "multivol.7z.001")
		require.NoError(t, os.WriteFile(path, data, 0o600)) //nolint:gosec // fixed name in a temp dir.

		assertChecked(t, &xtractr.XFile{FilePath: path}, xtractr.ErrMissingVolume)
	})

	t.Run("lone 7z later volume", func(t *testing.T) {
		t.Parallel()

		data, err := os.ReadFile(filepath.Join("test_data", "multivol.7z.002"))
		require.NoError(t, err)

		dir := t.TempDir()
		path := filepath.Join(dir, "multivol.7z.002")
		require.NoError(t, os.WriteFile(path, data, 0o600)) //nolint:gosec // fixed name in a temp dir.

		assertChecked(t, &xtractr.XFile{FilePath: path}, xtractr.ErrMissingVolume)
		err = xtractr.CheckVolumes(&xtractr.XFile{FilePath: path})
		require.ErrorContains(t, err, "multivol.7z.001")
	})

	t.Run("bad 7z header", func(t *testing.T) {
		t.Parallel()

		data, err := os.ReadFile(filepath.Join("test_data", "multivol.7z.001"))
		require.NoError(t, err)

		data[10] ^= 0xff

		dir := t.TempDir()
		path := filepath.Join(dir, "multivol.7z.001")
		require.NoError(t, os.WriteFile(path, data, 0o600)) //nolint:gosec // fixed name in a temp dir.

		assertHeaderError(t, path)
	})

	t.Run("truncated 7z header", func(t *testing.T) {
		t.Parallel()

		data, err := os.ReadFile(filepath.Join("test_data", "multivol.7z.001"))
		require.NoError(t, err)

		dir := t.TempDir()
		path := filepath.Join(dir, "multivol.7z.001")
		require.NoError(t, os.WriteFile(path, data[:20], 0o600)) //nolint:gosec // fixed name in a temp dir.

		assertHeaderError(t, path)
	})

	t.Run("zip", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "notes.zip")
		require.NoError(t, os.WriteFile(path, []byte("not a zip"), 0o600))
		assertChecked(t, &xtractr.XFile{FilePath: path})
	})

	t.Run("password", func(t *testing.T) {
		t.Parallel()

		assertChecked(t, &xtractr.XFile{
			FilePath:  filepath.Join("test_data", "archive.rar"),
			Password:  "wrong-password",
			Passwords: []string{"some_password"},
		})
	})
}

// assertChecked checks CheckVolumes and that the archive directory is unchanged.
// wrapped errors are optional; none means the call returns nil.
func assertChecked(t *testing.T, xFile *xtractr.XFile, wrapped ...error) {
	t.Helper()

	dir := filepath.Dir(xFile.FilePath)
	before := namesIn(t, dir)

	err := xtractr.CheckVolumes(xFile)
	if len(wrapped) == 0 {
		require.NoError(t, err)
	}

	for _, want := range wrapped {
		require.ErrorIs(t, err, want)
	}

	if len(wrapped) > 0 {
		assert.False(t, xtractr.IsLimitError(err), "a missing volume is not a configured limit: %v", err)
	}

	assert.Equal(t, before, namesIn(t, dir))
}

func assertHeaderError(t *testing.T, path string) {
	t.Helper()

	dir := filepath.Dir(path)
	before := namesIn(t, dir)

	err := xtractr.CheckVolumes(&xtractr.XFile{FilePath: path})
	require.Error(t, err)
	require.NotErrorIs(t, err, xtractr.ErrMissingVolume)
	assert.False(t, xtractr.IsLimitError(err), "a bad header is not a configured limit: %v", err)
	assert.Equal(t, before, namesIn(t, dir))
}

func namesIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}
