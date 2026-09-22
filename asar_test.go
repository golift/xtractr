package xtractr_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/asar"
	"golift.io/xtractr"
)

func TestExtractASAR(t *testing.T) {
	t.Parallel()

	for _, workers := range []int{0, 1, 4} {
		t.Run(fmt.Sprintf("FileWorkers=%d", workers), func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			archive := filepath.Join(dir, "app.asar")
			writeASAR(t, archive, map[string]any{
				"files": map[string]any{
					"README.txt": packedASAR("0", 5, nil),
					"dir": map[string]any{
						"files": map[string]any{
							"nested.txt": packedASAR("5", 3, nil),
						},
					},
					"a.txt": packedASAR("8", 4, nil),
					"b.txt": packedASAR("8", 4, nil),
					"bin":   packedASAR("12", 3, map[string]any{"executable": true}),
					"link":  map[string]any{"link": "bin"},
				},
			}, []byte("hello"), []byte("yes"), []byte("same"), []byte("run"))

			reader, err := asar.Open(archive)
			require.NoError(t, err)
			require.NotEmpty(t, reader.Files)
			require.NoError(t, reader.Close())

			out := filepath.Join(dir, "out")
			size, files, err := xtractr.ExtractASAR(&xtractr.XFile{
				FilePath:    archive,
				OutputDir:   out,
				FileMode:    0o600,
				DirMode:     0o700,
				FileWorkers: workers,
			})
			require.NoError(t, err)
			assert.Equal(t, uint64(5+3+4+4+3), size)
			assert.NotEmpty(t, files)

			assert.Equal(t, "hello", readFile(t, filepath.Join(out, "README.txt")))
			assert.Equal(t, "yes", readFile(t, filepath.Join(out, "dir", "nested.txt")))
			assert.Equal(t, "same", readFile(t, filepath.Join(out, "a.txt")))
			assert.Equal(t, "same", readFile(t, filepath.Join(out, "b.txt")))
			assert.Equal(t, "run", readFile(t, filepath.Join(out, "bin")))

			target, err := os.Readlink(filepath.Join(out, "link"))
			require.NoError(t, err)
			assert.Equal(t, "bin", filepath.ToSlash(target))

			if runtime.GOOS != "windows" {
				info, statErr := os.Stat(filepath.Join(out, "bin"))
				require.NoError(t, statErr)
				assert.NotEqual(t, os.FileMode(0), info.Mode()&0o111)
			}
		})
	}
}

func TestExtractASARUnpackedSibling(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "app.asar")
	writeASAR(t, archive, map[string]any{
		"files": map[string]any{
			"packed.txt":   packedASAR("0", 3, nil),
			"native.node":  map[string]any{"unpacked": true, "size": 6},
			"missing.node": map[string]any{"unpacked": true, "size": 1},
		},
	}, []byte("ok\n"))

	unpacked := archive + ".unpacked"
	require.NoError(t, os.MkdirAll(unpacked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(unpacked, "native.node"), []byte("binary"), 0o600))

	_, _, err := xtractr.ExtractASAR(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: filepath.Join(dir, "missing"),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing.node")

	out := filepath.Join(dir, "out")

	writeASAR(t, archive, map[string]any{
		"files": map[string]any{
			"packed.txt":  packedASAR("0", 3, nil),
			"native.node": map[string]any{"unpacked": true, "size": 6},
		},
	}, []byte("ok\n"))

	size, _, err := xtractr.ExtractASAR(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(3+6), size)
	assert.Equal(t, "ok\n", readFile(t, filepath.Join(out, "packed.txt")))
	assert.Equal(t, "binary", readFile(t, filepath.Join(out, "native.node")))
}

func TestExtractASARSymlinkEscape(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "app.asar")
	writeASAR(t, archive, map[string]any{
		"files": map[string]any{
			"evil": map[string]any{"link": "../outside"},
		},
	})

	_, _, err := xtractr.ExtractASAR(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)
}

func TestExtractFileASAR(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "app.asar")
	writeASAR(t, archive, map[string]any{
		"files": map[string]any{
			"hello.txt": packedASAR("0", 5, nil),
		},
	}, []byte("hello"))

	size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: filepath.Join(dir, "out"),
		FileMode:  0o600,
		DirMode:   0o700,
	})
	require.NoError(t, err)
	assert.Equal(t, uint64(5), size)
	assert.NotEmpty(t, files)
	assert.Equal(t, []string{archive}, archives)
	assert.Equal(t, "hello", readFile(t, filepath.Join(dir, "out", "hello.txt")))
}

func packedASAR(offset string, size int, extra map[string]any) map[string]any {
	entry := map[string]any{"offset": offset, "size": size}
	maps.Copy(entry, extra)

	return entry
}

func writeASAR(t *testing.T, path string, header map[string]any, blobs ...[]byte) {
	t.Helper()

	raw, err := json.Marshal(header)
	require.NoError(t, err)

	headerPickle := pickleString(string(raw))
	buf := bytes.NewBuffer(pickleUInt32(uint32(len(headerPickle))))
	_, err = buf.Write(headerPickle)
	require.NoError(t, err)

	for _, blob := range blobs {
		_, err = buf.Write(blob)
		require.NoError(t, err)
	}

	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}

func pickleUInt32(value uint32) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf[0:4], 4)
	binary.LittleEndian.PutUint32(buf[4:8], value)

	return buf
}

func pickleString(text string) []byte {
	str := []byte(text)
	pad := (4 - len(str)%4) % 4
	payload := 4 + len(str) + pad
	buf := make([]byte, 4+payload)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(payload))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(str)))
	copy(buf[8:], str)

	return buf
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(data)
}
