//go:build unix

package xtractr_test

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestExtractASARUnpackedFIFO(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "app.asar")
	writeUnpackedASAR(t, archive)
	require.NoError(t, syscall.Mkfifo(filepath.Join(archive+".unpacked", "native.node"), 0o600))

	errCh := make(chan error, 1)

	go func() {
		_, _, _, err := xtractr.ExtractASAR(&xtractr.XFile{
			FilePath:  archive,
			OutputDir: filepath.Join(dir, "out"),
			FileMode:  0o600,
			DirMode:   0o700,
		})
		errCh <- err
	}()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
	case <-time.After(5 * time.Second):
		t.Fatal("extraction blocked opening a FIFO in the unpacked sibling")
	}
}
