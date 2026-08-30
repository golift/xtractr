package xtractr_test

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestQueueSkipsZipSymlinkExtras(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := os.Symlink("target", filepath.Join(dir, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	inner := tinyZipBytes(t, "hello.txt", "from-inner")
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{"payload.zip": inner}, map[string]string{
		"a.zip": "payload.zip",
		"b.zip": "payload.zip",
		"c.zip": "payload.zip",
	})

	resp := extractQueueJob(t, dir, 0, 0)
	require.NoError(t, resp.Error)
	require.Equal(t, 1, resp.Extras.Count(), "symlink-named extras must not be queued")

	out := firstExisting(t, resp.Output, dir+xtractr.DefaultSuffix)
	require.FileExists(t, filepath.Join(out, "payload.zip"))
	require.FileExists(t, filepath.Join(out, "hello.txt"))
}

func TestQueueAllowSymlinksDoesNotRecurseArchiveMemberLinks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := os.Symlink("target", filepath.Join(dir, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	inner := tinyZipBytes(t, "hello.txt", "from-inner")
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{"payload.zip": inner}, map[string]string{
		"a.zip": "payload.zip",
		"b.zip": "payload.zip",
	})

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:     xtractr.Filter{Path: dir, AllowSymlinks: true},
		TempFolder: true,
		CBChannel:  make(chan *xtractr.Response, 2),
	}
	_, err = queue.Extract(job)
	require.NoError(t, err)
	resp := waitExtract(t, job.CBChannel)
	require.NoError(t, resp.Error)
	require.Equal(t, 1, resp.Extras.Count(), "archive-member zip symlinks must not be extras")
}

func TestQueueMaxNestedRejectsTooManyExtras(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{
		"one.zip":   tinyZipBytes(t, "one.txt", "1"),
		"two.zip":   tinyZipBytes(t, "two.txt", "2"),
		"three.zip": tinyZipBytes(t, "three.txt", "3"),
	}, nil)

	resp := extractQueueJob(t, dir, 2, 0)
	require.ErrorIs(t, resp.Error, xtractr.ErrMaxNested)
}

func TestQueueMaxNestedAllowsAtLimit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{
		"one.zip": tinyZipBytes(t, "one.txt", "1"),
		"two.zip": tinyZipBytes(t, "two.txt", "2"),
	}, nil)

	resp := extractQueueJob(t, dir, 2, 0)
	require.NoError(t, resp.Error)
	require.Equal(t, 2, resp.Extras.Count())

	out := firstExisting(t, resp.Output, dir+xtractr.DefaultSuffix)
	require.FileExists(t, filepath.Join(out, "one.txt"))
	require.FileExists(t, filepath.Join(out, "two.txt"))
}

func TestQueueExtrasMaxDepthSkipsDeepArchives(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{
		"keep.txt":            []byte("surface"),
		"a/b/c/inner.zip":     tinyZipBytes(t, "deep.txt", "buried"),
		"shallow/sibling.zip": tinyZipBytes(t, "near.txt", "ok"),
	}, nil)

	resp := extractQueueJob(t, dir, 64, 2)
	require.NoError(t, resp.Error)

	out := firstExisting(t, resp.Output, dir+xtractr.DefaultSuffix)
	require.FileExists(t, filepath.Join(out, "keep.txt"))
	require.FileExists(t, filepath.Join(out, "a", "b", "c", "inner.zip"))
	require.NoFileExists(t, filepath.Join(out, "a", "b", "c", "deep.txt"))
	require.FileExists(t, filepath.Join(out, "near.txt"))
}

func TestQueueSiblingArchivesGetOwnMaxFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeOuterZip(t, filepath.Join(dir, "one.zip"), map[string][]byte{"one.txt": []byte("1")}, nil)
	writeOuterZip(t, filepath.Join(dir, "two.zip"), map[string][]byte{"two.txt": []byte("2")}, nil)

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:           xtractr.Filter{Path: dir},
		TempFolder:       true,
		DisableRecursion: true,
		MaxFiles:         1,
		CBChannel:        make(chan *xtractr.Response, 2),
	}
	_, err := queue.Extract(job)
	require.NoError(t, err)

	resp := waitExtract(t, job.CBChannel)
	require.NoError(t, resp.Error)

	out := firstExisting(t, resp.Output, dir+xtractr.DefaultSuffix)
	require.FileExists(t, filepath.Join(out, "one.txt"))
	require.FileExists(t, filepath.Join(out, "two.txt"))
}

func TestQueueArchiveBudgetSharesFilesWithExtras(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{
		"inner.zip": tinyZipBytes(t, "hello.txt", "from-inner"),
	}, nil)

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: true,
		MaxFiles:   1,
		CBChannel:  make(chan *xtractr.Response, 2),
	}
	_, err := queue.Extract(job)
	require.NoError(t, err)
	require.ErrorIs(t, waitExtract(t, job.CBChannel).Error, xtractr.ErrMaxFiles)
}

func TestQueueArchiveBudgetSharesBytesWithExtras(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	inner := tinyZipBytes(t, "big.txt", string(make([]byte, 4096)))
	writeOuterZip(t, src, map[string][]byte{"inner.zip": inner}, nil)

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: true,
		MaxBytes:   uint64(len(inner) + 64),
		CBChannel:  make(chan *xtractr.Response, 2),
	}
	_, err := queue.Extract(job)
	require.NoError(t, err)
	require.ErrorIs(t, waitExtract(t, job.CBChannel).Error, xtractr.ErrMaxBytes)
}

func TestQueueExtrasMaxDepthOverrideExtractsDeep(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := filepath.Join(dir, "outer.zip")
	writeOuterZip(t, src, map[string][]byte{
		"keep.txt":            []byte("surface"),
		"a/b/c/inner.zip":     tinyZipBytes(t, "deep.txt", "buried"),
		"shallow/sibling.zip": tinyZipBytes(t, "near.txt", "ok"),
	}, nil)

	resp := extractQueueJob(t, dir, 64, 3)
	require.NoError(t, resp.Error)

	out := firstExisting(t, resp.Output, dir+xtractr.DefaultSuffix)
	require.FileExists(t, filepath.Join(out, "deep.txt"))
	require.FileExists(t, filepath.Join(out, "near.txt"))
}

func extractQueueJob(t *testing.T, dir string, maxNested, extrasMaxDepth int) *xtractr.Response {
	t.Helper()

	queue := xtractr.NewQueue(&xtractr.Config{
		Logger:   xtractr.NoLogger(),
		FileMode: 0o600,
		DirMode:  0o700,
	})
	defer queue.Stop()

	job := &xtractr.Xtract{
		Filter:         xtractr.Filter{Path: dir},
		TempFolder:     true,
		MaxNested:      maxNested,
		ExtrasMaxDepth: extrasMaxDepth,
		CBChannel:      make(chan *xtractr.Response, 2),
	}

	_, err := queue.Extract(job)
	require.NoError(t, err)

	return waitExtract(t, job.CBChannel)
}

func tinyZipBytes(t *testing.T, name, body string) []byte {
	t.Helper()

	buf := new(bytes.Buffer)
	zipWriter := zip.NewWriter(buf)
	writer, err := zipWriter.Create(name)
	require.NoError(t, err)
	_, err = writer.Write([]byte(body))
	require.NoError(t, err)
	require.NoError(t, zipWriter.Close())

	return buf.Bytes()
}

func writeOuterZip(t *testing.T, dest string, files map[string][]byte, links map[string]string) {
	t.Helper()

	archiveFile, err := os.Create(dest)
	require.NoError(t, err)

	zipWriter := zip.NewWriter(archiveFile)

	for name, payload := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()}
		writer, createErr := zipWriter.CreateHeader(header)
		require.NoError(t, createErr)

		_, err = writer.Write(payload)
		require.NoError(t, err)
	}

	for name, target := range links {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()}
		header.SetMode(0o755 | fs.ModeSymlink)
		writer, createErr := zipWriter.CreateHeader(header)
		require.NoError(t, createErr)

		_, err = writer.Write([]byte(target))
		require.NoError(t, err)
	}

	require.NoError(t, zipWriter.Close())
	require.NoError(t, archiveFile.Close())
}

func firstExisting(t *testing.T, paths ...string) string {
	t.Helper()

	for _, path := range paths {
		if path == "" {
			continue
		}

		_, err := os.Stat(path)
		if err == nil {
			return path
		}
	}

	t.Fatalf("none of the extract output paths exist: %v", paths)

	return ""
}
