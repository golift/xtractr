package xtractr_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

// TestSiblingPrefixBoundaryBypass ensures archive entries like ../out_evil/file
// cannot escape OutputDir via a strings.HasPrefix sibling-prefix trick
// (OutputDir=/tmp/out, escaped=/tmp/out_evil/...). See Unpackerr/unpackerr#641.
func TestSiblingPrefixBoundaryBypass(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	out := filepath.Join(base, "out")
	require.NoError(t, os.MkdirAll(out, 0o750))

	zipPath := filepath.Join(base, "malicious.zip")
	tarPath := filepath.Join(base, "malicious.tar")

	require.NoError(t, writeZipWithTraversal(zipPath, "../out_evil/escaped_zip.txt", "ZIP-BYPASS"))
	require.NoError(t, writeTarWithTraversal(tarPath, "../out_evil/escaped_tar.txt", "TAR-BYPASS"))

	_, _, zipErr := xtractr.ExtractZIP(&xtractr.XFile{
		FilePath:  zipPath,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	_, _, tarErr := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  tarPath,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})

	require.ErrorIs(t, zipErr, xtractr.ErrInvalidPath)
	require.ErrorIs(t, tarErr, xtractr.ErrInvalidPath)

	_, zipReadErr := os.ReadFile(filepath.Join(base, "out_evil", "escaped_zip.txt"))
	_, tarReadErr := os.ReadFile(filepath.Join(base, "out_evil", "escaped_tar.txt"))

	require.ErrorIs(t, zipReadErr, os.ErrNotExist)
	require.ErrorIs(t, tarReadErr, os.ErrNotExist)
}

func writeZipWithTraversal(path, name, payload string) error {
	var buf bytes.Buffer

	zipWriter := zip.NewWriter(&buf)

	writer, err := zipWriter.Create(name)
	if err != nil {
		return fmt.Errorf("create zip entry: %w", err)
	}

	_, err = writer.Write([]byte(payload))
	if err != nil {
		return fmt.Errorf("write zip payload: %w", err)
	}

	err = zipWriter.Close()
	if err != nil {
		return fmt.Errorf("close zip: %w", err)
	}

	err = os.WriteFile(path, buf.Bytes(), 0o600)
	if err != nil {
		return fmt.Errorf("write zip file: %w", err)
	}

	return nil
}

func writeTarWithTraversal(path, name, payload string) error {
	var buf bytes.Buffer

	tarWriter := tar.NewWriter(&buf)
	data := []byte(payload)

	err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))})
	if err != nil {
		return fmt.Errorf("write tar header: %w", err)
	}

	_, err = tarWriter.Write(data)
	if err != nil {
		return fmt.Errorf("write tar payload: %w", err)
	}

	err = tarWriter.Close()
	if err != nil {
		return fmt.Errorf("close tar: %w", err)
	}

	err = os.WriteFile(path, buf.Bytes(), 0o600)
	if err != nil {
		return fmt.Errorf("write tar file: %w", err)
	}

	return nil
}
