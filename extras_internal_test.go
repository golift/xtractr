package xtractr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPick(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, pick(0, 0))
	assert.Equal(t, 8, pick(8, 0))
	assert.Equal(t, 4, pick(0, 4))
	assert.Equal(t, 8, pick(8, 4))
	assert.Equal(t, -1, pick(-1, 0))
	assert.Equal(t, -1, pick(0, -1))
	assert.Equal(t, -1, pick(-1, 4))
	assert.Equal(t, uint64(3), pick(uint64(0), uint64(3)))
}
