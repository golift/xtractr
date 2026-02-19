package xtractr //nolint:testpackage // necessary for testing truncateToBytes

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestTruncateToBytes verifies truncateToBytes (used by TruncatePathForFS)
// and would have caught the negative maxBytes bug (infinite loop/panic).
func TestTruncateToBytes(t *testing.T) {
	t.Parallel()

	t.Run("negative_maxBytes_returns_empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, truncateToBytes("hello", -1))
		assert.Empty(t, truncateToBytes("any string", -100))
	})

	t.Run("zero_maxBytes_returns_empty", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, truncateToBytes("hello", 0))
	})

	t.Run("positive_within_limit_returns_unchanged", func(t *testing.T) {
		t.Parallel()

		str := "hello"
		assert.Equal(t, str, truncateToBytes(str, 5))
		assert.Equal(t, str, truncateToBytes(str, 10))
	})

	t.Run("truncates_at_byte_boundary", func(t *testing.T) {
		t.Parallel()

		str := "hello"
		assert.Equal(t, "hell", truncateToBytes(str, 4))
		assert.Equal(t, "he", truncateToBytes(str, 2))
	})

	t.Run("utf8_rune_boundary", func(t *testing.T) {
		t.Parallel()
		// "é" is 2 bytes in UTF-8; must not cut mid-rune
		str := "café"
		assert.Equal(t, "café", truncateToBytes(str, 5))
		// Truncate to 4 bytes: cannot keep full "é" (2 bytes), so drop it → "caf" (3 bytes)
		out4 := truncateToBytes(str, 4)
		assert.Equal(t, "caf", out4)
		assert.Len(t, out4, 3)
		assert.Equal(t, "caf", truncateToBytes(str, 3))
	})

	t.Run("long_string", func(t *testing.T) {
		t.Parallel()

		str := strings.Repeat("a", 300)
		out := truncateToBytes(str, 255)
		assert.Len(t, out, 255)
		assert.Equal(t, strings.Repeat("a", 255), out)
	})
}
