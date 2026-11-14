package xtractr_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golift.io/xtractr"
)

func TestExtractRAR(t *testing.T) {
	t.Parallel()

	size, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  "./test_data/archive.rar",
		OutputDir: t.TempDir(),
		Password:  "testing", // one of these is right. :)
		Passwords: []string{"testingmore", "some_password", "some_other"},
	})

	require.NoError(t, err)
	assert.Equal(t, testDataSize, size)
	assert.Len(t, archives, 1)
	assert.Len(t, files, len(filesInTestArchive))
}

// TestExtractRARPasswordRetryKeepsConfig guards against the password-retry
// path dropping the caller-provided XFile configuration: the retries used to
// build a bare XFile, silently losing the logger (and SquashRoot, progress
// callbacks, etc.) for every attempt after the first.
func TestExtractRARPasswordRetryKeepsConfig(t *testing.T) {
	t.Parallel()

	logger := &countingLogger{t: t}
	xFile := &xtractr.XFile{
		FilePath:  "./test_data/archive.rar",
		OutputDir: t.TempDir(),
		Passwords: []string{"wrong-password", "some_password"}, // correct password is second.
	}
	xFile.SetLogger(logger)

	size, files, archives, err := xtractr.ExtractRAR(xFile)

	require.NoError(t, err)
	assert.Equal(t, testDataSize, size)
	assert.Len(t, archives, 1)
	assert.Len(t, files, len(filesInTestArchive))
	assert.Positive(t, logger.debugCount.Load(),
		"the winning password retry must keep the caller's logger")
}

// countingLogger counts log lines so tests can verify a logger is wired up.
type countingLogger struct {
	t          *testing.T
	printCount atomic.Int64
	debugCount atomic.Int64
}

func (c *countingLogger) Printf(format string, v ...any) {
	c.printCount.Add(1)
	c.t.Logf(format, v...)
}

func (c *countingLogger) Debugf(format string, v ...any) {
	c.debugCount.Add(1)
	c.t.Logf(format, v...)
}

// TestExtractRARMultiVolume guards against the regression where only the entry
// file (instead of every volume) was returned in the archive list, which left
// the sibling parts of a multi-part archive orphaned during cleanup.
func TestExtractRARMultiVolume(t *testing.T) {
	t.Parallel()

	parts, err := filepath.Glob(filepath.Join("test_data", "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, parts, "multi-volume rar fixtures must be present")

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join("test_data", "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(parts), "every volume must be returned for cleanup")

	for _, part := range parts {
		assert.Contains(t, archives, part, "volume %s must be present in the archive list", part)
	}
}

// TestExtractRARMultiVolumeAbsolutePath verifies that when the caller passes an
// absolute path to the entry archive, every returned volume is an absolute path
// resolved beside the entry file. rardecode reports bare volume basenames, so
// this exercises normalizeVolumes joining them onto the entry archive's
// directory rather than the process working directory.
func TestExtractRARMultiVolumeAbsolutePath(t *testing.T) {
	t.Parallel()

	dir, err := filepath.Abs("test_data")
	require.NoError(t, err)

	parts, err := filepath.Glob(filepath.Join(dir, "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, parts, "multi-volume rar fixtures must be present")

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join(dir, "multivol.part1.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(parts), "every volume must be returned for cleanup")

	for _, part := range parts {
		assert.Contains(t, archives, part, "volume %s must be returned as an absolute path", part)
	}
}

// TestExtractRARMultiVolumeOldScheme covers the legacy RAR volume naming scheme
// (multivol.rar, multivol.r00, multivol.r01, ...) which is exactly the layout
// this PR's cleanup fix must handle. To avoid committing additional binary
// fixtures, the existing multi-part payloads are symlinked under the old names
// inside a temp dir; rardecode follows the chain and every volume must be
// returned for cleanup.
func TestExtractRARMultiVolumeOldScheme(t *testing.T) {
	t.Parallel()

	src, err := filepath.Abs("test_data")
	require.NoError(t, err)

	payloads, err := filepath.Glob(filepath.Join(src, "multivol.part*.rar"))
	require.NoError(t, err)
	require.NotEmpty(t, payloads, "multi-volume rar fixtures must be present")

	dir := t.TempDir()
	wantVolumes := make([]string, len(payloads))

	for idx, payload := range payloads {
		name := fmt.Sprintf("multivol.r%02d", idx-1)
		if idx == 0 {
			name = "multivol.rar" // first volume of the old scheme is the .rar file
		}

		link := filepath.Join(dir, name)

		err := os.Symlink(payload, link)
		if err != nil {
			t.Skipf("symlinks unavailable on this platform: %v", err)
		}

		wantVolumes[idx] = link
	}

	_, files, archives, err := xtractr.ExtractRAR(&xtractr.XFile{
		FilePath:  filepath.Join(dir, "multivol.rar"),
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, files, "files should have been extracted")
	assert.Len(t, archives, len(wantVolumes), "every old-scheme volume must be returned for cleanup")

	for _, want := range wantVolumes {
		assert.Contains(t, archives, want, "old-scheme volume %s must be present in the archive list", want)
	}
}

// TestExtractRARMissingVolume covers a multi-volume set whose later volumes
// have not arrived yet, for example while a sync tool is still copying it.
// The header scan used to treat the failed volume open as the end of the
// archive, so the claimed size was compared to only the volumes it could
// open and a configured MaxRatio reported a compression ratio cap hit. That
// is a limit error, which callers treat as final, although the archive is
// fine and a later attempt succeeds.
func TestExtractRARMissingVolume(t *testing.T) {
	t.Parallel()

	const volumes = 4 // test_data/multivol.part1.rar to part4.rar

	// The complete fixture set unpacks to less than its packed size (ratio about 0.93),
	// so a MaxRatio of 1 passes it. Only a size measured against too few volumes trips it.
	_, _, archives, err := xtractr.ExtractRAR(rarRatioFile(t, filepath.Join("test_data", "multivol.part1.rar")))
	require.NoError(t, err, "the complete set must pass the ratio cap")
	require.Len(t, archives, volumes)

	for _, scheme := range []string{"new", "old"} {
		for skip := 1; skip < volumes; skip++ {
			t.Run(scheme+"/"+rarVolumeName(scheme, skip), func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()

				for idx := range volumes {
					if idx == skip {
						continue
					}

					data, err := os.ReadFile(filepath.Join("test_data", rarVolumeName("new", idx)))
					require.NoError(t, err)

					dest := filepath.Join(dir, rarVolumeName(scheme, idx))
					require.NoError(t, os.WriteFile(dest, data, 0o600)) //nolint:gosec // fixed names in a temp dir.
				}

				_, _, archives, err := xtractr.ExtractRAR(rarRatioFile(t, filepath.Join(dir, rarVolumeName(scheme, 0))))
				require.ErrorIs(t, err, xtractr.ErrMissingVolume)
				require.ErrorIs(t, err, fs.ErrNotExist, "the volume open error must be kept")
				assert.False(t, xtractr.IsLimitError(err), "a missing volume is not a configured limit: %v", err)
				assert.Empty(t, archives)
			})
		}
	}
}

// rarVolumeName is the name of volume idx (from 0) in the new (.partN.rar)
// or old (.rar, .r00, .r01, ...) naming scheme.
func rarVolumeName(scheme string, idx int) string {
	switch {
	case scheme == "new":
		return fmt.Sprintf("multivol.part%d.rar", idx+1)
	case idx == 0:
		return "multivol.rar"
	default:
		return fmt.Sprintf("multivol.r%02d", idx-1)
	}
}

func rarRatioFile(t *testing.T, path string) *xtractr.XFile {
	t.Helper()

	return &xtractr.XFile{
		FilePath:  path,
		OutputDir: t.TempDir(),
		FileMode:  xtractr.DefaultFileMode,
		DirMode:   xtractr.DefaultDirMode,
		MaxRatio:  1,
	}
}
