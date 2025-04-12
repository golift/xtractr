package xtractr_test

import (
	"log"
	"os"
	"path/filepath"
	"testing"

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
	testDataSize = int64(20770)
)

type testLogger struct{ t *testing.T }

func (l *testLogger) Debugf(msg string, format ...interface{}) {
	l.t.Helper()

	msg = "[DEBUG] " + msg
	//	l.t.Logf(msg, format...)
	log.Printf(msg, format...)
}

func (l *testLogger) Printf(msg string, format ...interface{}) {
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
	defer openFile.Close()

	_, err = openFile.Write(data)

	return err
}
