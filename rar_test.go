package xtractr_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestExtractRAR(t *testing.T) {
	t.Parallel()

	name := t.TempDir()
	defer os.RemoveAll(name)

	size, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  "./test_data/archive.rar",
		OutputDir: name,
		Password:  "testing", // one of these is right. :)
		Passwords: []string{"testingmore", "some_password", "some_other"},
	})
	require.NoError(t, err)
	assert.Equal(t, testDataSize, size)
	assert.Len(t, archives, 1)
	assert.Len(t, files, len(filesInTestArchive))
}
