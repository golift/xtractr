package xtractr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWeExtractedAnISOIncludesImg(t *testing.T) {
	t.Parallel()

	assert.True(t, weExtractedAnISO(&Response{
		Archives: ArchiveList{"dir": {"dir/disc.img"}},
	}))
	assert.True(t, weExtractedAnISO(&Response{
		Archives: ArchiveList{"dir": {"dir/DISC.ISO"}},
	}))
	assert.False(t, weExtractedAnISO(&Response{
		Archives: ArchiveList{"dir": {"dir/notes.zip"}},
	}))
}
