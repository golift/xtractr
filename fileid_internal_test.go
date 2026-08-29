package xtractr

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRememberFileHardlinks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.zip")
	alias := filepath.Join(dir, "alias.zip")

	require.NoError(t, os.WriteFile(orig, []byte("pk"), 0o600))

	err := os.Link(orig, alias)
	if err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}

	other := filepath.Join(dir, "other.zip")
	require.NoError(t, os.WriteFile(other, []byte("pk2"), 0o600))

	seen := make(map[string]struct{})
	assert.True(t, rememberFile(seen, orig))
	assert.False(t, rememberFile(seen, alias))
	assert.True(t, rememberFile(seen, other))
}

func TestRememberFileSymlinkAliases(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.zip")
	require.NoError(t, os.WriteFile(orig, []byte("pk"), 0o600))

	link := filepath.Join(dir, "link.zip")

	err := os.Symlink(orig, link)
	if err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	seen := make(map[string]struct{})
	assert.True(t, rememberFile(seen, orig))
	assert.False(t, rememberFile(seen, link))
}

func TestExtrasAcceptSkipsRecursionAndHardlinks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	orig := filepath.Join(dir, "orig.zip")
	alias := filepath.Join(dir, "alias.zip")
	cue := filepath.Join(dir, "copied.cue.zip")
	other := filepath.Join(dir, "other.zip")

	require.NoError(t, os.WriteFile(orig, []byte("pk"), 0o600))
	require.NoError(t, os.WriteFile(cue, []byte("pk"), 0o600))
	require.NoError(t, os.WriteFile(other, []byte("pk2"), 0o600))

	err := os.Link(orig, alias)
	if err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}

	accept := extrasAccept([]string{cue})
	assert.False(t, accept(ArchiveCandidate{Path: cue}))
	assert.True(t, accept(ArchiveCandidate{Path: orig}))
	assert.False(t, accept(ArchiveCandidate{Path: alias}))
	assert.True(t, accept(ArchiveCandidate{Path: other}))
}
