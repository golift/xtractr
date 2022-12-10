package xtractr_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"golift.io/xtractr"
)

func TestExtractRAR(t *testing.T) {
	t.Parallel()

	name, err := os.MkdirTemp(".", "xtractr_test_*_data")
	if err != nil {
		t.Fatalf("could not make temporary directory: %v", err)
	}
	defer os.RemoveAll(name) //nolint:wsl

	size, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  "./test_data/archive.rar",
		OutputDir: name,
		Password:  "testing", // one of these is right. :)
		Passwords: []string{"testingmore", "some_password", "some_other"},
	})
	assert.NoError(t, err)
	assert.Equal(t, testDataSize, size)
	assert.Equal(t, 1, len(archives))
	assert.Equal(t, len(filesInTestArchive), len(files))
}
