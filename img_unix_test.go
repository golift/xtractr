//go:build unix

package xtractr_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestFindImgSkipsNamedPipe(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pipe := filepath.Join(dir, "raw.img")
	require.NoError(t, syscall.Mkfifo(pipe, 0o600))

	disc := filepath.Join(dir, "disc.img")
	writeSignatureAt(t, disc, 0x8001, []byte("CD001"))

	link := filepath.Join(dir, "link.img")
	require.NoError(t, os.Symlink(pipe, link))

	found := findBeforeTimeout(t, &xtractr.Filter{Path: dir, AllowSymlinks: true})
	assert.Equal(t, xtractr.ArchiveList{dir: {disc}}, found)

	assert.Empty(t, findBeforeTimeout(t, &xtractr.Filter{Path: pipe}))
	assert.Empty(t, findBeforeTimeout(t, &xtractr.Filter{Path: link, AllowSymlinks: true}))
}

func findBeforeTimeout(t *testing.T, filter *xtractr.Filter) xtractr.ArchiveList {
	t.Helper()

	found := make(chan xtractr.ArchiveList, 1)

	go func() {
		found <- xtractr.FindCompressedFiles(*filter)
	}()

	select {
	case list := <-found:
		return list
	case <-time.After(2 * time.Second):
		t.Fatal("search blocked on a named pipe")
	}

	return nil
}
