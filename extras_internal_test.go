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

func TestExtrasDepth(t *testing.T) {
	t.Parallel()

	assert.Equal(t, DefaultExtrasMaxDepth, extrasDepth(0, 0))
	assert.Equal(t, 4, extrasDepth(4, 0))
	assert.Equal(t, 3, extrasDepth(0, 3))
	assert.Equal(t, 4, extrasDepth(4, 3))
	assert.Equal(t, 0, extrasDepth(-1, 0))
	assert.Equal(t, 0, extrasDepth(0, -1))
	assert.Equal(t, 0, extrasDepth(-1, 3))
}
