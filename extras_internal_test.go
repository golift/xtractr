package xtractr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtrasCap(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultMaxNested, extrasCap(0, 0))
	assert.Equal(t, 8, extrasCap(8, 0))
	assert.Equal(t, 4, extrasCap(0, 4))
	assert.Equal(t, 8, extrasCap(8, 4))
	assert.Equal(t, 0, extrasCap(-1, 0))
	assert.Equal(t, 0, extrasCap(0, -1))
	assert.Equal(t, 0, extrasCap(-1, 4))
}
