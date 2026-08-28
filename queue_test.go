package xtractr_test

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

//nolint:gochecknoglobals
var filesInTestArchive = []string{
	"doc.go",
	"files.go",
	"queue.go",
	"rar.go",
	"start.go",
	"zip.go",
}

const (
	testFile     = "test_data/archive.rar"
	testDataSize = uint64(20770)
)

type testLogger struct{ t *testing.T }

func (l *testLogger) Debugf(msg string, format ...any) {
	l.t.Helper()

	msg = "[DEBUG] " + msg
	//	l.t.Logf(msg, format...)
	log.Printf(msg, format...)
}

func (l *testLogger) Printf(msg string, format ...any) {
	l.t.Helper()

	msg = "[INFO] " + msg
	//	l.t.Logf(msg, format...)
	log.Printf(msg, format...)
}

func TestWithTempFolder(t *testing.T) {
	t.Parallel()

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "SomeItem",
		Filter:     xtractr.Filter{Path: testSetupTestDir(t)},
		TempFolder: true,
		DeleteOrig: false,
		Password:   "some_password",
		LogFile:    true,
		CBChannel:  make(chan *xtractr.Response),
	}

	depth, err := queue.Extract(xFile)
	require.NoError(t, err, "why is there an error?!")
	assert.Equal(t, 1, depth, "there should be 1 item queued now")

	for resp := range xFile.CBChannel {
		require.NoError(t, resp.Error, "the test archives should extract without any error")
		assert.Len(t, resp.Archives, 4, "four directories have archives in them")

		if resp.Done {
			assert.Len(t, resp.NewFiles, len(filesInTestArchive)*4+4,
				"wrong count of files were extracted, log files must be written too!")
			assert.Equal(t, testDataSize*4, resp.Size, "wrong amount of data was written")

			break
		}
	}

	// test written files here?
	// each directory should have its own files.
	_ = os.RemoveAll(xFile.Path)
	_ = os.RemoveAll(xFile.Path + xtractr.DefaultSuffix)
}

func TestNoTempFolder(t *testing.T) {
	t.Parallel()

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "SomeItem",
		Filter:     xtractr.Filter{Path: testSetupTestDir(t)},
		TempFolder: false,
		DeleteOrig: true,
		Password:   "some_password",
		LogFile:    false,
		CBChannel:  make(chan *xtractr.Response),
	}

	depth, err := queue.Extract(xFile)
	require.NoError(t, err, "why is there an error?!")
	assert.Equal(t, 1, depth, "there should be 1 item queued now")

	for resp := range xFile.CBChannel {
		require.NoError(t, resp.Error, "the test archives should extract without any error")
		assert.Len(t, resp.Archives, 4, "four directories have archives in them")

		if resp.Done {
			assert.Len(t, resp.NewFiles, len(filesInTestArchive)*4, "wrong count of files were extracted")
			assert.Equal(t, testDataSize*4, resp.Size, "wrong amount of data was written")

			break
		}
	}

	// test written files here?
	// each directory should have its own files.
	_ = os.RemoveAll(xFile.Path)
	_ = os.RemoveAll(xFile.Path + xtractr.DefaultSuffix)
}

// TestRefusedExistingDestination is the reported bug, now observable: a file
// already occupying the destination name is kept, the extracted copy is
// discarded, the run reports success, and Response.Refused names the pair.
func TestRefusedExistingDestination(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")
	require.NoError(t, makeFile(t, testFileData, filepath.Join(dir, "archive.rar")))

	const junk = "not from the archive"

	blocker := filepath.Join(dir, "doc.go")
	require.NoError(t, os.WriteFile(blocker, []byte(junk), 0o600))

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "Refused",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: false,
		DeleteOrig: false,
		Password:   "some_password",
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()

	var done *xtractr.Response

loop:
	for {
		select {
		case resp, ok := <-xFile.CBChannel:
			require.True(t, ok, "callback channel closed before extraction completed")
			require.NoError(t, resp.Error)

			if resp.Done {
				done = resp
				break loop
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for extraction to complete")
		}
	}

	require.Len(t, done.Refused, 1)
	assert.Equal(t, filepath.Join(dir+xtractr.DefaultSuffix, "doc.go"), done.Refused[0].Src)
	assert.Equal(t, blocker, done.Refused[0].Dest)

	got, err := os.ReadFile(blocker)
	require.NoError(t, err)
	assert.Equal(t, junk, string(got))
}

// TestFinalDestMovedBack reports the move destination when the temp folder is
// moved back into the download path (TempFolder=false).
func TestFinalDestMovedBack(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")
	require.NoError(t, makeFile(t, testFileData, filepath.Join(dir, "archive.rar")))

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "FinalDest",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: false,
		DeleteOrig: false,
		Password:   "some_password",
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	done := waitFinalResponse(t, xFile.CBChannel)
	require.NoError(t, done.Error)
	// Move-back writes into the search path itself; the archive-extension
	// strip only applies to the kept temp folder's final name.
	assert.Equal(t, map[string]string{dir: dir}, done.FinalDests)
	assert.DirExists(t, done.FinalDests[dir])
	assert.FileExists(t, filepath.Join(dir, "doc.go"))
}

// TestFinalDestTempFolder reports the final unsuffixed output folder when the
// temp folder is kept (TempFolder=true).
func TestFinalDestTempFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")
	require.NoError(t, makeFile(t, testFileData, filepath.Join(dir, "archive.rar")))

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}, TryNames: true})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "FinalDestTmp",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: true,
		DeleteOrig: false,
		Password:   "some_password",
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	done := waitFinalResponse(t, xFile.CBChannel)
	require.NoError(t, done.Error)
	require.NotEmpty(t, done.FinalDests, "FinalDests must name the moved output folder")
	assert.Equal(t, map[string]string{dir: done.Output}, done.FinalDests)
	assert.NotContains(t, done.FinalDests[dir], xtractr.DefaultSuffix)
	assert.DirExists(t, done.FinalDests[dir])
}

// TestFinalDestsMultiFolder reports each per-folder destination when a search
// path holds archives in more than one subfolder (a multi-key ArchiveList).
func TestFinalDestsMultiFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")

	subA := filepath.Join(dir, "cd1")
	subB := filepath.Join(dir, "cd2")

	require.NoError(t, os.MkdirAll(subA, 0o750))
	require.NoError(t, os.MkdirAll(subB, 0o750))
	require.NoError(t, makeFile(t, testFileData, filepath.Join(subA, "archive.rar")))
	require.NoError(t, makeFile(t, testFileData, filepath.Join(subB, "archive.rar")))

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "FinalDestMulti",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: false,
		DeleteOrig: false,
		Password:   "some_password",
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	done := waitFinalResponse(t, xFile.CBChannel)
	require.NoError(t, done.Error)
	// Both per-folder destinations are reported, keyed by their input folder.
	assert.Equal(t, map[string]string{subA: subA, subB: subB}, done.FinalDests)
	assert.DirExists(t, done.FinalDests[subA])
	assert.DirExists(t, done.FinalDests[subB])
}

// TestFinalDestsTempFolderMultiFolder keeps one destination for the renamed
// output tree, keyed by the original search path (TempFolder=true does not
// produce a per-subfolder entry).
func TestFinalDestsTempFolderMultiFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")

	subA := filepath.Join(dir, "cd1")
	subB := filepath.Join(dir, "cd2")

	require.NoError(t, os.MkdirAll(subA, 0o750))
	require.NoError(t, os.MkdirAll(subB, 0o750))
	require.NoError(t, makeFile(t, testFileData, filepath.Join(subA, "archive.rar")))
	require.NoError(t, makeFile(t, testFileData, filepath.Join(subB, "archive.rar")))

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}, TryNames: true})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "FinalDestTmpMulti",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: true,
		DeleteOrig: false,
		Password:   "some_password",
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	done := waitFinalResponse(t, xFile.CBChannel)
	require.NoError(t, done.Error)
	require.Len(t, done.FinalDests, 1)
	assert.Equal(t, done.Output, done.FinalDests[dir])
	assert.NotContains(t, done.FinalDests[dir], xtractr.DefaultSuffix)
	assert.DirExists(t, done.FinalDests[dir])
}

func waitFinalResponse(t *testing.T, chResponse chan *xtractr.Response) *xtractr.Response {
	t.Helper()

	// Multi-folder move-back sleeps fsSyncDelay (10s) per folder.
	timeout := time.NewTimer(60 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case resp, ok := <-chResponse:
			require.True(t, ok, "callback channel closed before extraction completed")

			if resp.Done {
				return resp
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for extraction to complete")
		}
	}
}

// testSetupTestDir creates a temp directory with 4 copies of a rar archive in it.
func testSetupTestDir(t *testing.T) string {
	t.Helper()

	name := t.TempDir()

	testFileData, err := os.ReadFile(testFile)
	require.NoError(t, err, "reading test data file failed")

	for _, sub := range []string{"subDir1", "subDir2", "subDir3"} {
		err = os.MkdirAll(filepath.Join(name, "subDirectory", sub), xtractr.DefaultDirMode)
		require.NoError(t, err, "creating temp directory failed")

		fileName := filepath.Join(name, "subDirectory", sub, sub+"_archive.rar")
		require.NoError(t, makeFile(t, testFileData, fileName), "creating test archive failed")
	}

	err = makeFile(t, testFileData, filepath.Join(name, "subDirectory", "primary_arechive.rar"))
	require.NoError(t, err, "creating test archive failed")

	return name
}

//nolint:wrapcheck
func makeFile(t *testing.T, data []byte, fileName string) error {
	t.Helper()

	openFile, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer safeCloser(t, openFile)

	_, err = openFile.Write(data)

	return err
}

// TestMultiVolumeCleanup is the end-to-end regression guard for the bug where
// only the entry volume of a multi-part archive was deleted, orphaning the
// remaining parts. It extracts a multi-volume rar with DeleteOrig enabled and
// asserts that every part file is removed from disk.
func TestMultiVolumeCleanup(t *testing.T) {
	t.Parallel()

	srcParts, err := filepath.Glob(filepath.Join("test_data", "multivol.part*.rar"))
	require.NoError(t, err, "reading multi-volume fixtures failed")
	require.GreaterOrEqual(t, len(srcParts), 2, "fixture must contain multiple volumes")

	dir := t.TempDir()
	parts := make([]string, len(srcParts))

	for idx, src := range srcParts {
		data, err := os.ReadFile(src)
		require.NoError(t, err, "reading fixture part failed")

		parts[idx] = filepath.Join(dir, filepath.Base(src))
		require.NoError(t, makeFile(t, data, parts[idx]), "copying fixture part failed")
	}

	queue := xtractr.NewQueue(&xtractr.Config{Logger: &testLogger{t: t}})
	defer queue.Stop()

	xFile := &xtractr.Xtract{
		Name:       "MultiVolume",
		Filter:     xtractr.Filter{Path: dir},
		TempFolder: false,
		DeleteOrig: true,
		CBChannel:  make(chan *xtractr.Response),
	}

	_, err = queue.Extract(xFile)
	require.NoError(t, err)

	timeout := time.NewTimer(15 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case resp, ok := <-xFile.CBChannel:
			require.True(t, ok, "callback channel closed before extraction completed")
			require.NoError(t, resp.Error, "the multi-volume archive should extract without error")

			if resp.Done {
				goto done
			}
		case <-timeout.C:
			t.Fatal("timed out waiting for multi-volume extraction to complete")
		}
	}

done:

	// Every volume must be gone, not just the entry part.
	for _, part := range parts {
		assert.NoFileExists(t, part, "volume %s should have been deleted", filepath.Base(part))
	}
}
