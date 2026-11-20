package xtractr_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestImgIsArchiveByName(t *testing.T) {
	t.Parallel()

	assert.True(t, xtractr.IsArchiveFile("disc.img"))
	assert.True(t, xtractr.IsArchiveFile("DISC.IMG"))
	assert.Contains(t, xtractr.SupportedExtensions(), ".img")
}

func TestFindCompressedFilesImgNeedsDiscSignature(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	disc := filepath.Join(dir, "disc.img")
	udf := filepath.Join(dir, "movie.IMG")
	raw := filepath.Join(dir, "raw.img")
	gzipImg := filepath.Join(dir, "data.img")

	writeSignatureAt(t, disc, 0x8001, []byte("CD001"))
	writeSignatureAt(t, udf, 0x8001, []byte("BEA01"))
	require.NoError(t, os.WriteFile(raw, []byte("not a disc image"), 0o600))
	require.NoError(t, os.WriteFile(gzipImg, []byte{0x1F, 0x8B, 0x08, 0x00}, 0o600))

	var names []string

	for _, files := range xtractr.FindCompressedFiles(xtractr.Filter{Path: dir}) {
		for _, file := range files {
			names = append(names, filepath.Base(file))
		}
	}

	assert.ElementsMatch(t, []string{"disc.img", "movie.IMG"}, names)

	found := xtractr.FindCompressedFiles(xtractr.Filter{Path: disc})
	assert.Equal(t, xtractr.ArchiveList{disc: {disc}}, found)

	assert.Empty(t, xtractr.FindCompressedFiles(xtractr.Filter{Path: raw}),
		"a .img passed in directly is listed only when it is a disc")
	assert.Empty(t, xtractr.FindCompressedFiles(xtractr.Filter{Path: dir, ExcludeSuffix: []string{".img"}}))
}

func TestExtractFileTriesImgWithoutSignature(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	img := filepath.Join(dir, "raw.img")
	require.NoError(t, os.WriteFile(img, []byte("not a disc image"), 0o600))

	_, _, _, err := xtractr.ExtractFile(&xtractr.XFile{ //nolint:dogsled // only the error matters here.
		FilePath:  img,
		OutputDir: filepath.Join(dir, "out"),
	})

	var extracted *xtractr.ExtractError
	require.ErrorAs(t, err, &extracted)
	assert.Equal(t, "iso", extracted.ArchiveType)
}

func writeSignatureAt(t *testing.T, path string, offset int, magic []byte) {
	t.Helper()

	data := make([]byte, offset+len(magic))
	copy(data[offset:], magic)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}
