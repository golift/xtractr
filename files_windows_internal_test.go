//go:build windows

package xtractr

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenExtractFileRefusesNULDevice(t *testing.T) {
	t.Parallel()

	dest := filepath.Join(t.TempDir(), "NUL")

	_, _, err := openExtractFile(dest, 0o600)
	require.Error(t, err)
	require.ErrorIs(t, err, errExtractNotRegular)
}
