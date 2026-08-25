//go:build unix

package xtractr

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenExtractFileRefusesFifo(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "member")
	require.NoError(t, syscall.Mkfifo(dest, 0o600))

	_, _, err := openExtractFile(dest, 0o600)
	require.Error(t, err)
	require.ErrorIs(t, err, errExtractNotRegular)

	info, err := os.Lstat(dest)
	require.NoError(t, err)
	require.NotEqual(t, os.FileMode(0), info.Mode()&os.ModeNamedPipe)
}
