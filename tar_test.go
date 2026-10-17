package xtractr_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsnet/compress/bzip2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/ulikunitz/xz"
	"golift.io/xtractr"
)

type compressor interface {
	Compress(t *testing.T, sourceDir string, destFileBase string) error
}

type (
	tarCompressor     struct{}
	tarZCompressor    struct{}
	tarBzipCompressor struct{}
	tarXZCompressor   struct{}
	tarGzipCompressor struct{}
)

func TestTar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		compressor compressor
		extension  string
	}{
		{"tar", &tarCompressor{}, "tar"},
		{"tarZ", &tarZCompressor{}, "tar.z"},
		{"tarBzip", &tarBzipCompressor{}, "tar.bz2"},
		{"tarXZ", &tarXZCompressor{}, "tar.xz"},
		{"tarGzip", &tarGzipCompressor{}, "tar.gz"},
	}

	testFilesInfo := createTestFiles(t)
	require.NotNil(t, testFilesInfo)

	for i := range tests {
		test := tests[i]
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archiveBase := filepath.Join(testFilesInfo.dstFilesDir, "archive")
			err := test.compressor.Compress(t, testFilesInfo.srcFilesDir, archiveBase)
			require.NoError(t, err)

			extractDir := filepath.Join(testFilesInfo.dstFilesDir, test.name)
			err = os.Mkdir(extractDir, 0o700)
			require.NoError(t, err)

			size, files, archives, err := xtractr.ExtractFile(&xtractr.XFile{
				FilePath:  archiveBase + "." + test.extension,
				OutputDir: filepath.Clean(extractDir),
				FileMode:  0o600,
				DirMode:   0o700,
			})
			require.NoError(t, err)
			assert.Equal(t, testFilesInfo.dataSize, size)
			assert.Len(t, files, testFilesInfo.fileCount)
			assert.Len(t, archives, testFilesInfo.archiveCount)
		})
	}
}

// TestTarSymlinks ensures symlink (and hard-link) members are restored as links
// instead of empty regular files. Regression for https://github.com/golift/xtractr/issues/153
func TestTarSymlinks(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	archivePath := filepath.Join(tmp, "libs.tar.gz")
	extractDir := filepath.Join(tmp, "out")

	require.NoError(t, createSymlinkTarGzip(archivePath))

	_, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:  archivePath,
		OutputDir: extractDir,
		FileMode:  0o755,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Len(t, files, 4)

	realFile := filepath.Join(extractDir, "libfoo.so.1.2.3")
	info, err := os.Lstat(realFile)
	require.NoError(t, err)
	assert.Zero(t, info.Mode()&os.ModeSymlink, "real library should not be a symlink")
	assert.Positive(t, info.Size())

	for linkName, wantTarget := range map[string]string{
		"libfoo.so.1": "libfoo.so.1.2.3",
		"libfoo.so":   "libfoo.so.1",
	} {
		linkPath := filepath.Join(extractDir, linkName)
		info, err = os.Lstat(linkPath)
		require.NoError(t, err, linkName)
		require.NotZero(t, info.Mode()&os.ModeSymlink, "%s should be a symlink, not a regular file", linkName)

		got, err := os.Readlink(linkPath)
		require.NoError(t, err, linkName)
		assert.Equal(t, wantTarget, got, linkName)
	}

	hardPath := filepath.Join(extractDir, "libfoo.hard")
	info, err = os.Lstat(hardPath)
	require.NoError(t, err)
	// Hard link when supported; otherwise we fall back to a symlink.
	if info.Mode()&os.ModeSymlink != 0 {
		got, err := os.Readlink(hardPath)
		require.NoError(t, err)
		assert.Equal(t, "libfoo.so.1.2.3", got)
	} else {
		assert.Equal(t, mustFileSize(t, realFile), info.Size())
	}
}

// TestTarSymlinkEscape rejects symlink and hard-link targets that leave OutputDir.
func TestTarSymlinkEscape(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	for _, testCase := range []struct {
		name     string
		linkName string
		kind     byte
	}{
		{
			name:     "absolute_symlink",
			linkName: filepath.Join(tmp, "outside"),
			kind:     tar.TypeSymlink,
		},
		{
			name:     "relative_symlink",
			linkName: "../outside",
			kind:     tar.TypeSymlink,
		},
		{
			name:     "absolute_hardlink",
			linkName: filepath.Join(tmp, "outside"),
			kind:     tar.TypeLink,
		},
		{
			// filepath.Rel-based checks must reject .. escapes that HasPrefix can miss.
			name:     "relative_hardlink_escape",
			linkName: "../outside",
			kind:     tar.TypeLink,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			archivePath := filepath.Join(tmp, testCase.name+".tar.gz")
			extractDir := filepath.Join(tmp, testCase.name+"-out")

			require.NoError(t, createLinkOnlyTarGzip(archivePath, "escape.link", testCase.linkName, testCase.kind))

			// Call ExtractTarGzip directly: ExtractFile falls back to plain gzip on error.
			_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
				FilePath:  archivePath,
				OutputDir: extractDir,
				FileMode:  0o755,
				DirMode:   0o755,
			})
			require.Error(t, err)
			assert.ErrorIs(t, err, xtractr.ErrInvalidPath)
		})
	}
}

// TestTarSymlinkCleanBypass is the chained-link case also covered for cpio.
// createSymlink is shared, so tar must reject it too: foo/bar -> ".." makes
// foo/bar/../victim look inside OutputDir after filepath.Clean, and the kernel
// resolves it outside.
func TestTarSymlinkCleanBypass(t *testing.T) {
	t.Parallel()

	err := os.Symlink("target", filepath.Join(t.TempDir(), "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	t.Run("existing", func(t *testing.T) {
		t.Parallel()
		assertTarCleanBypassRejected(t, true)
	})

	t.Run("dangling", func(t *testing.T) {
		t.Parallel()
		assertTarCleanBypassRejected(t, false)
	})
}

func assertTarCleanBypassRejected(t *testing.T, victimExists bool) {
	t.Helper()

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	if victimExists {
		require.NoError(t, os.MkdirAll(victim, 0o750))
	}

	archive := filepath.Join(root, "bypass.tar.gz")
	require.NoError(t, writeTarGzipSymlinkBypass(archive))

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)

	got, err := os.Readlink(filepath.Join(out, "foo", "bar"))
	require.NoError(t, err)
	assert.Equal(t, "..", got)
	assertPathInside(t, out, filepath.Join(out, "foo", "bar"))

	assertAbsent(t, filepath.Join(out, "baz"))
	assertAbsent(t, filepath.Join(victim, "pwned"))

	if !victimExists {
		assertAbsent(t, victim)
	}
}

func writeTarGzipSymlinkBypass(dest string) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create tar: %w", err)
	}

	gzipWriter := gzip.NewWriter(archiveFile)
	tarWriter := tar.NewWriter(gzipWriter)

	links := []struct {
		name   string
		target string
	}{
		{name: "foo/bar", target: ".."},
		{name: "baz", target: "foo/bar/../victim"},
	}
	for _, link := range links {
		err = tarWriter.WriteHeader(&tar.Header{
			Name:     link.name,
			Linkname: link.target,
			Mode:     0o777,
			Typeflag: tar.TypeSymlink,
			ModTime:  time.Now(),
		})
		if err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = archiveFile.Close()

			return fmt.Errorf("tar link %s: %w", link.name, err)
		}
	}

	payload := []byte("pwned")

	err = tarWriter.WriteHeader(&tar.Header{
		Name:     "baz/pwned",
		Mode:     0o644,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	})
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = archiveFile.Close()

		return fmt.Errorf("tar file header: %w", err)
	}

	_, err = tarWriter.Write(payload)
	if err != nil {
		_ = tarWriter.Close()
		_ = gzipWriter.Close()
		_ = archiveFile.Close()

		return fmt.Errorf("tar file payload: %w", err)
	}

	err = tarWriter.Close()
	if err != nil {
		_ = gzipWriter.Close()
		_ = archiveFile.Close()

		return fmt.Errorf("close tar: %w", err)
	}

	err = gzipWriter.Close()
	if err != nil {
		_ = archiveFile.Close()

		return fmt.Errorf("close gzip: %w", err)
	}

	err = archiveFile.Close()
	if err != nil {
		return fmt.Errorf("close tar file: %w", err)
	}

	return nil
}

// TestTarSymlinkRetargetEscape is a link that stays inside OutputDir only while
// an earlier symlink is deep. Replacing that symlink leaves the first one
// pointing at a sibling of OutputDir. "foo/../victim" still cleans inside, so
// the lexical check alone does not reject it.
func TestTarSymlinkRetargetEscape(t *testing.T) {
	t.Parallel()

	err := os.Symlink("target", filepath.Join(t.TempDir(), "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	t.Run("shorten deep link", func(t *testing.T) {
		t.Parallel()
		assertTarRetargetRejected(t, []tarMember{
			{name: "sub", dir: true},
			{name: "sub/deep", dir: true},
			{name: "foo", link: "sub/deep"},
			{name: "bar", link: "foo/../../victim"},
			{name: "foo", link: "sub"},
		})
	})

	t.Run("climb before link exists", func(t *testing.T) {
		t.Parallel()
		assertTarRetargetRejected(t, []tarMember{
			{name: "bar", link: "foo/../victim"},
			{name: "foo", link: "."},
		})
	})
}

func assertTarRetargetRejected(t *testing.T, members []tarMember) {
	t.Helper()

	root := t.TempDir()
	out := filepath.Join(root, "out")
	archive := filepath.Join(root, "retarget.tar.gz")
	require.NoError(t, writeTarGzipMembers(archive, members))

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)
	assertAbsent(t, filepath.Join(out, "bar"))
	assertAbsent(t, filepath.Join(root, "victim"))
}

type tarMember struct {
	name string
	link string // symlink text, or the hard link's target when hard is set.
	dir  bool
	hard bool
	data string // regular file content.
}

func writeTarGzipMembers(dest string, members []tarMember) error {
	return writeTarMembers(dest, true, members)
}

// writeTarMembers writes a tar, gzip-compressed when compress is set. A plain
// tar is useful with ExtractFile: when a .tar.gz extract fails, ExtractFile
// falls back to plain gunzip and hides the tar error.
func writeTarMembers(dest string, compress bool, members []tarMember) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create tar: %w", err)
	}

	var gzipWriter io.WriteCloser = nopWriteCloser{archiveFile}
	if compress {
		gzipWriter = gzip.NewWriter(archiveFile)
	}

	tarWriter := tar.NewWriter(gzipWriter)

	for _, member := range members {
		err = tarWriter.WriteHeader(member.header())
		if err == nil && member.data != "" {
			_, err = tarWriter.Write([]byte(member.data))
		}

		if err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = archiveFile.Close()

			return fmt.Errorf("tar member %s: %w", member.name, err)
		}
	}

	err = tarWriter.Close()
	if err != nil {
		_ = gzipWriter.Close()
		_ = archiveFile.Close()

		return fmt.Errorf("close tar: %w", err)
	}

	err = gzipWriter.Close()
	if err != nil {
		_ = archiveFile.Close()

		return fmt.Errorf("close gzip: %w", err)
	}

	err = archiveFile.Close()
	if err != nil {
		return fmt.Errorf("close tar file: %w", err)
	}

	return nil
}

func (m tarMember) header() *tar.Header {
	header := &tar.Header{
		Name:     m.name,
		Mode:     0o755,
		ModTime:  time.Now(),
		Typeflag: tar.TypeReg,
		Size:     int64(len(m.data)),
	}

	switch {
	case m.dir:
		header.Typeflag = tar.TypeDir
	case m.hard:
		header.Typeflag = tar.TypeLink
		header.Linkname = m.link
	case m.link != "":
		header.Typeflag = tar.TypeSymlink
		header.Linkname = m.link
	}

	return header
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// TestTarPlantedSymlinkClimb arms a link that was already in OutputDir.
// "bar -> existing" follows "missing/../victim", and "missing -> ." makes that
// text resolve outside. Replacing an existing directory with the shallower
// link does the same.
func TestTarPlantedSymlinkClimb(t *testing.T) {
	t.Parallel()

	err := os.Symlink("target", filepath.Join(t.TempDir(), "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	t.Run("follow planted link", func(t *testing.T) {
		t.Parallel()
		assertTarPlantedClimbRejected(t, false, []tarMember{
			{name: "bar", link: "existing"},
			{name: "missing", link: "."},
		})
	})

	t.Run("arm planted link first", func(t *testing.T) {
		t.Parallel()
		assertTarPlantedClimbRejected(t, false, []tarMember{
			{name: "missing", link: "."},
			{name: "bar", link: "existing"},
		})
	})

	t.Run("replace directory", func(t *testing.T) {
		t.Parallel()
		assertTarPlantedClimbRejected(t, true, []tarMember{
			{name: "a", link: "."},
		})
	})
}

func assertTarPlantedClimbRejected(t *testing.T, withDir bool, members []tarMember) {
	t.Helper()

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))

	planted := "missing/../victim"

	if withDir {
		require.NoError(t, os.MkdirAll(filepath.Join(out, "a"), 0o750))

		planted = "a/../victim"
	}

	require.NoError(t, os.Symlink(planted, filepath.Join(out, "existing")))

	archive := filepath.Join(root, "planted.tar.gz")
	require.NoError(t, writeTarGzipMembers(archive, members))

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)
	assertAbsent(t, filepath.Join(out, "bar"))
	assertAbsent(t, filepath.Join(out, "missing"))

	if withDir {
		// The directory the refused link replaced is put back.
		info, statErr := os.Lstat(filepath.Join(out, "a"))
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
	}

	resolved, evalErr := filepath.EvalSymlinks(filepath.Join(out, "existing"))
	if evalErr == nil {
		assert.NotEqual(t, victim, resolved)
		assertPathInside(t, out, filepath.Join(out, "existing"))
	}
}

// TestTarShortenedPlantedClimb is a planted link whose text cleans to a
// path outside OutputDir but resolves inside through a deeper link. The
// archive replaces that link with a shorter one, with or without an earlier
// link entry; the extract fails and the deeper link is put back.
func TestTarShortenedPlantedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	for name, members := range map[string][]tarMember{
		"after another link": {{name: "readme", link: "README"}, {name: "foo", link: "sub"}},
		"as the first link":  {{name: "foo", link: "sub"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			out := filepath.Join(root, "out")
			victim := filepath.Join(root, "victim")

			require.NoError(t, os.MkdirAll(filepath.Join(out, "sub", "deep"), 0o750))
			require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
			require.NoError(t, os.Symlink("sub/deep", filepath.Join(out, "foo")))
			require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

			archive := filepath.Join(root, "planted.tar.gz")
			require.NoError(t, writeTarGzipMembers(archive, members))

			_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
				FilePath:  archive,
				OutputDir: out,
				FileMode:  0o644,
				DirMode:   0o755,
			})
			require.ErrorIs(t, err, xtractr.ErrInvalidPath)

			got, err := os.Readlink(filepath.Join(out, "foo"))
			require.NoError(t, err)
			// Windows os.Symlink stores the text with backslashes.
			assert.Equal(t, filepath.FromSlash("sub/deep"), got)

			resolved, evalErr := filepath.EvalSymlinks(filepath.Join(out, "existing"))
			if evalErr == nil {
				assert.NotEqual(t, victim, resolved)
				assertPathInside(t, out, filepath.Join(out, "existing"))
			}
		})
	}
}

// TestTarRejectedLinkKeepsExisting is a link entry at the path of a file that
// was already in OutputDir. The text is rejected, and the file stays.
func TestTarRejectedLinkKeepsExisting(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	keep := filepath.Join(out, "keep.txt")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o600))

	archive := filepath.Join(root, "evil.tar.gz")
	require.NoError(t, writeTarGzipMembers(archive, []tarMember{
		{name: "keep.txt", link: "../../etc/passwd"},
	}))

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)

	data, err := os.ReadFile(keep)
	require.NoError(t, err)
	assert.Equal(t, "mine", string(data))
}

// plantDanglingClimb lays out OutputDir with foo -> missing and
// existing -> foo/../../victim, which resolves nowhere until a directory
// named missing appears and then resolves to victim, beside OutputDir.
func plantDanglingClimb(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.Symlink("missing", filepath.Join(out, "foo")))
	require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

	_, err := filepath.EvalSymlinks(filepath.Join(out, "existing"))
	require.Error(t, err, "existing must dangle before the extract")

	return out, victim
}

// assertNotResolvingTo passes when link does not resolve, or resolves to
// something other than victim. Both sides are resolved: macOS keeps TempDir
// under /var -> /private/var.
func assertNotResolvingTo(t *testing.T, link, victim string) {
	t.Helper()

	realVictim, err := filepath.EvalSymlinks(victim)
	require.NoError(t, err)

	resolved, err := filepath.EvalSymlinks(link)
	if err == nil {
		assert.NotEqual(t, realVictim, resolved, "%s resolves to the victim", link)
	}
}

// TestTarDirArmsPlantedClimb is a directory member, not a link, that arms a
// planted link: foo -> missing dangles, existing -> foo/../../victim resolves
// nowhere, and a plain missing/ makes it resolve beside OutputDir. The
// directory has to be audited like a link would be, whether it is its own
// member, a file's parent, or followed by a link (which used to collect the
// planted links only then, with missing already in place).
func TestTarDirArmsPlantedClimb(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	for name, members := range map[string][]tarMember{
		"directory member": {{name: "missing/", dir: true}},
		"file parent":      {{name: "missing/a.txt"}},
		"then a link":      {{name: "missing/", dir: true}, {name: "other", link: "missing"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, victim := plantDanglingClimb(t)

			archive := filepath.Join(filepath.Dir(out), "evil.tar.gz")
			require.NoError(t, writeTarGzipMembers(archive, members))

			_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
				FilePath:  archive,
				OutputDir: out,
				FileMode:  0o644,
				DirMode:   0o755,
			})
			require.ErrorIs(t, err, xtractr.ErrInvalidPath)

			assertAbsent(t, filepath.Join(out, "missing"))
			assertNotResolvingTo(t, filepath.Join(out, "existing"), victim)
		})
	}
}

// TestTarDirUnderPlantedClimbStaysInside is the other side: a directory at a
// path a planted link looked up is fine when the link still lands inside.
// existing -> missing/../keep resolves nowhere, then to OutputDir/keep.
func TestTarDirUnderPlantedClimbStaysInside(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.Symlink("missing/../keep", filepath.Join(out, "existing")))

	archive := filepath.Join(root, "fine.tar.gz")
	require.NoError(t, writeTarGzipMembers(archive, []tarMember{{name: "missing/a.txt"}, {name: "keep"}}))

	_, files, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Len(t, files, 2)
	require.FileExists(t, filepath.Join(out, "missing", "a.txt"))
	assertPathInside(t, out, filepath.Join(out, "existing"))
}

// squashTar writes members to a plain tar beside out and extracts it into out
// with SquashRoot. Plain tar, because ExtractFile falls back to gunzip when a
// .tar.gz fails, which hides the tar error.
func squashTar(t *testing.T, out string, members []tarMember) error {
	t.Helper()

	_, err := squashTarFiles(t, out, members)

	return err
}

// squashTarFiles is squashTar returning the extracted paths as well.
func squashTarFiles(t *testing.T, out string, members []tarMember) ([]string, error) {
	t.Helper()

	archive := filepath.Join(filepath.Dir(out), "squash.tar")
	require.NoError(t, writeTarMembers(archive, false, members))

	_, files, _, err := xtractr.ExtractFile(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	if err != nil {
		return files, fmt.Errorf("extract: %w", err)
	}

	return files, nil
}

// TestTarSquashRootLinkEscape covers links that are inside OutputDir where the
// archive put them and outside it once SquashRoot moves the root's children up
// one level: the text gets a component shorter, and the names filled in under
// OutputDir change what other links resolve to. Each case has a victim beside
// OutputDir, so a miss shows up as a link that resolves to it.
func TestTarSquashRootLinkEscape(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	t.Run("escapes", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "victim"), []byte("pwned"), 0o600))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/file.txt"},
			{name: "pkg/up", link: "../victim"},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "up"))
	})

	// ../pkg/victim cleans back inside the root and the walk re-enters the
	// root while it exists. After the move, out/up -> ../pkg/victim is a
	// sibling of OutputDir.
	t.Run("climbs back into root", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o750))
		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "victim"), []byte("pwned"), 0o600))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/file.txt"},
			{name: "pkg/up", link: "../pkg/victim"},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "up"))
	})

	// out/target already exists, so the move refuses pkg/target and keeps the
	// planted link; pkg/link -> target moves and follows it.
	t.Run("follows kept entry", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "victim"), []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("../victim", filepath.Join(out, "target")))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/target/file"},
			{name: "pkg/link", link: "target"},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "link"))
	})

	// pkg/missing -> "." is fine where it is created. Moved to out/missing it
	// arms the planted out/existing -> missing/../victim.
	t.Run("arms planted link", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "victim"), []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("missing/../victim", filepath.Join(out, "existing")))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/file.txt"},
			{name: "pkg/missing", link: "."},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "missing"))
	})

	// No link in the archive at all. pkg/missing is a directory, and moved
	// to out/missing it arms the planted out/existing -> missing/../../victim
	// the way creating it there would; mkdirComponent refuses that, and the
	// move used to skip the planted links when root held no links.
	t.Run("directory arms planted link", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		victim := filepath.Join(root, "victim")

		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("missing/../../victim", filepath.Join(out, "existing")))

		err := squashTar(t, out, []tarMember{{name: "pkg/missing/file.txt"}})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "missing"))
		assertRegularFile(t, filepath.Join(out, "pkg", "missing", "file.txt"))
		assertNotResolvingTo(t, filepath.Join(out, "existing"), victim)
	})

	// pkg/link -> b/c/planted goes through the moved pkg/b -> kept into a
	// planted link that already pointed outside. Treating the not-yet-moved
	// b as a missing name would pass this lexically.
	t.Run("follows moved link into kept one", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(filepath.Join(out, "kept", "c"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "victim"), []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("../../../victim", filepath.Join(out, "kept", "c", "planted")))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/b", link: "kept"},
			{name: "pkg/link", link: "b/c/planted"},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "link"))
	})

	// The only entry is a symlink to a directory already in OutputDir. The
	// move would follow it and relocate that directory's contents, including
	// a link that only pointed inside from where it was. Nothing moves.
	t.Run("symlink root", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(filepath.Join(out, "real"), 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(root, "victim"), []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("../victim", filepath.Join(out, "real", "up")))

		err := squashTar(t, out, []tarMember{{name: "pkg", link: "real"}})
		require.NoError(t, err)
		assertAbsent(t, filepath.Join(out, "up"))

		got, err := os.Readlink(filepath.Join(out, "pkg"))
		require.NoError(t, err)
		assert.Equal(t, "real", got)

		_, err = os.Lstat(filepath.Join(out, "real", "up"))
		require.NoError(t, err)
	})

	t.Run("stays inside root", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/lib/real.txt"},
			{name: "pkg/bin/link", link: "../lib/real.txt"},
		})
		require.NoError(t, err)
		assertAbsent(t, filepath.Join(out, "pkg"))
		assertPathInside(t, out, filepath.Join(out, "bin", "link"))
	})

	// A moved link through another moved link is resolved against the root's
	// copy of b, not refused because out/b does not exist yet.
	t.Run("follows moved link", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		out := filepath.Join(root, "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		err := squashTar(t, out, []tarMember{
			{name: "pkg/sub/real.txt"},
			{name: "pkg/b", link: "sub"},
			{name: "pkg/link", link: "b/real.txt"},
		})
		require.NoError(t, err)
		assertAbsent(t, filepath.Join(out, "pkg"))
		assertPathInside(t, out, filepath.Join(out, "link"))
	})
}

// TestTarSquashRootSameNamedChild squashes roots with a child of the same
// name. The child's destination is the root's own path, which used to be
// occupied when the move got there; the child was refused, deleted with the
// root, and the extract came back empty with a nil error. The root is moved
// aside before its children now, so the child takes the name.
func TestTarSquashRootSameNamedChild(t *testing.T) {
	t.Parallel()

	t.Run("directory", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		files, err := squashTarFiles(t, out, []tarMember{{name: "foo/foo/bar.txt"}})
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(out, "foo")}, files)
		assertRegularFile(t, filepath.Join(out, "foo", "bar.txt"))
		assertDirNames(t, out, "foo")
	})

	t.Run("file", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		files, err := squashTarFiles(t, out, []tarMember{{name: "foo/foo"}})
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(out, "foo")}, files)
		assertRegularFile(t, filepath.Join(out, "foo"))
		assertDirNames(t, out, "foo")
	})

	t.Run("with sibling", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		files, err := squashTarFiles(t, out, []tarMember{
			{name: "foo/foo/bar.txt"},
			{name: "foo/other.txt"},
		})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{filepath.Join(out, "foo"), filepath.Join(out, "other.txt")}, files)
		assertRegularFile(t, filepath.Join(out, "foo", "bar.txt"))
		assertRegularFile(t, filepath.Join(out, "other.txt"))
		assertDirNames(t, out, "foo", "other.txt")
	})

	// A case-insensitive filesystem finds the root under the child's spelling
	// too. The child still takes the name once the root is aside.
	t.Run("other spelling", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		err := squashTar(t, out, []tarMember{{name: "foo/Foo/bar.txt"}})
		require.NoError(t, err)
		assertRegularFile(t, filepath.Join(out, "Foo", "bar.txt"))
		assertDirNames(t, out, "Foo")
	})

	// The aside folder is a sibling of the children's destinations, so a
	// child named like it would be refused and deleted with it, the way a
	// child named like the root was. The squash is refused instead, and the
	// files stay where the archive put them.
	t.Run("aside name", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		err := squashTar(t, out, []tarMember{
			{name: "foo/foo.xtractr_aside/bar.txt"},
			{name: "foo/other.txt"},
		})
		require.ErrorIs(t, err, xtractr.ErrAsideTaken)
		assertRegularFile(t, filepath.Join(out, "foo", "foo.xtractr_aside", "bar.txt"))
		assertRegularFile(t, filepath.Join(out, "foo", "other.txt"))
		assertDirNames(t, out, "foo")
	})

	// On a case-insensitive filesystem the child answers to the aside name
	// in another spelling, and the filesystem is what decides that: the
	// squash is refused there, and goes through where names are distinct.
	t.Run("aside name other spelling", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))

		err := squashTar(t, out, []tarMember{{name: "foo/FOO.XTRACTR_ASIDE/bar.txt"}})
		if caseInsensitiveDir(t, out) {
			require.ErrorIs(t, err, xtractr.ErrAsideTaken)
			assertRegularFile(t, filepath.Join(out, "foo", "FOO.XTRACTR_ASIDE", "bar.txt"))
			assertDirNames(t, out, "foo")

			return
		}

		require.NoError(t, err)
		assertRegularFile(t, filepath.Join(out, "FOO.XTRACTR_ASIDE", "bar.txt"))
		assertDirNames(t, out, "FOO.XTRACTR_ASIDE")
	})

	// A link already in OutputDir looked up the aside name and found nothing.
	// Renaming the root to it would make the link resolve through the moved
	// tree (here, out of OutputDir), a layout nothing audits, and a failed
	// move would leave it that way. The squash is refused before anything
	// is renamed.
	t.Run("aside name looked up", func(t *testing.T) {
		t.Parallel()
		skipWithoutSymlinks(t)

		root := t.TempDir()
		out := filepath.Join(root, "out")
		victim := filepath.Join(root, "victim")

		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
		require.NoError(t, os.Symlink("foo.xtractr_aside/../../victim", filepath.Join(out, "existing")))

		err := squashTar(t, out, []tarMember{{name: "foo/bar.txt"}})
		require.ErrorIs(t, err, xtractr.ErrAsideTaken)
		assertRegularFile(t, filepath.Join(out, "foo", "bar.txt"))
		assertDirNames(t, out, "existing", "foo")
		assertNotResolvingTo(t, filepath.Join(out, "existing"), victim)
	})

	// The link audit models the move. out/Foo is where foo/Foo/up ends up on
	// any filesystem, and ../../victim from there is outside OutputDir. On a
	// case-insensitive filesystem the root answers for out/Foo before the
	// move; counting that as an occupied name would place the link a level
	// deeper than it lands, and pass it.
	t.Run("other spelling escapes", func(t *testing.T) {
		t.Parallel()
		skipWithoutSymlinks(t)

		root := t.TempDir()
		out := filepath.Join(root, "out")
		victim := filepath.Join(root, "victim")

		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))

		err := squashTar(t, out, []tarMember{
			{name: "foo/Foo/bar.txt"},
			{name: "foo/Foo/up", link: "../../victim"},
		})
		require.ErrorIs(t, err, xtractr.ErrInvalidPath)
		assertAbsent(t, filepath.Join(out, "Foo", "up"))
		assertNotResolvingTo(t, filepath.Join(out, "foo", "Foo", "up"), victim)
	})

	// An existing entry under OutputDir is still an occupied name.
	t.Run("keeps existing", func(t *testing.T) {
		t.Parallel()

		out := filepath.Join(t.TempDir(), "out")
		require.NoError(t, os.MkdirAll(out, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(out, "other.txt"), []byte("mine"), 0o600))

		files, err := squashTarFiles(t, out, []tarMember{
			{name: "foo/foo/bar.txt"},
			{name: "foo/other.txt"},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{filepath.Join(out, "foo")}, files)
		assertRegularFile(t, filepath.Join(out, "foo", "bar.txt"))
		assertDirNames(t, out, "foo", "other.txt")

		data, err := os.ReadFile(filepath.Join(out, "other.txt"))
		require.NoError(t, err)
		assert.Equal(t, "mine", string(data))
	})
}

func assertRegularFile(t *testing.T, path string) {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "%s: mode %s", path, info.Mode())
}

// assertDirNames is the exact set of entries in dir: what the extract was
// meant to leave, and nothing it moved aside or left behind.
func assertDirNames(t *testing.T, dir string, names ...string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Name())
	}

	assert.ElementsMatch(t, names, got, "entries in %s", dir)
}

// caseInsensitiveDir reports whether dir is on a filesystem that finds a
// name under other spellings.
func caseInsensitiveDir(t *testing.T, dir string) bool {
	t.Helper()

	probe := filepath.Join(dir, "case-probe")
	require.NoError(t, os.WriteFile(probe, nil, 0o600))

	defer os.Remove(probe)

	_, err := os.Lstat(filepath.Join(dir, "CASE-PROBE"))

	return err == nil
}

// assertNoAside fails when an entry xtractr moved aside is still under dir.
func assertNoAside(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	for _, entry := range entries {
		assert.NotContains(t, entry.Name(), "_aside", "left behind in %s", dir)
	}
}

// TestTarRejectedLinkKeepsFile replaces a file with a link that passes the
// text checks and fails the audit: foo -> . arms the planted
// existing -> foo/../../victim. The link comes out, and the file goes back.
// It used to be deleted before the link was written, and stay deleted.
func TestTarRejectedLinkKeepsFile(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	victim := filepath.Join(root, "victim")
	keep := filepath.Join(out, "foo")

	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(victim, []byte("pwned"), 0o600))
	require.NoError(t, os.WriteFile(keep, []byte("mine"), 0o600))
	require.NoError(t, os.Symlink("foo/../../victim", filepath.Join(out, "existing")))

	archive := filepath.Join(root, "evil.tar.gz")
	require.NoError(t, writeTarGzipMembers(archive, []tarMember{{name: "foo", link: "."}}))

	_, _, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.ErrorIs(t, err, xtractr.ErrInvalidPath)

	assertRegularFile(t, keep)
	assertNoAside(t, out)
	assertNotResolvingTo(t, filepath.Join(out, "existing"), victim)

	data, err := os.ReadFile(keep)
	require.NoError(t, err)
	assert.Equal(t, "mine", string(data))
}

// TestTarHardLinkToAsideName is a hard link whose target is the name the
// file at its path is set aside under, with the target extracted after the
// link. The target is looked up before the file is set aside, so the link
// takes the symlink fallback and reads the new content once the target is
// written. It used to link the old file under the aside name, and keep the
// old data after the aside was discarded.
func TestTarHardLinkToAsideName(t *testing.T) {
	t.Parallel()
	skipWithoutSymlinks(t)

	root := t.TempDir()
	out := filepath.Join(root, "out")
	require.NoError(t, os.MkdirAll(out, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(out, "foo"), []byte("old"), 0o600))

	archive := filepath.Join(root, "hard.tar")
	require.NoError(t, writeTarMembers(archive, false, []tarMember{
		{name: "foo", link: "foo.xtractr_aside", hard: true},
		{name: "foo.xtractr_aside", data: "new"},
	}))

	_, _, err := xtractr.ExtractTar(&xtractr.XFile{
		FilePath:  archive,
		OutputDir: out,
		FileMode:  0o644,
		DirMode:   0o755,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(out, "foo"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))

	data, err = os.ReadFile(filepath.Join(out, "foo.xtractr_aside"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(data))
	assertDirNames(t, out, "foo", "foo.xtractr_aside")
}

// TestTarEmptyLinkSkipped warns and continues when a link target is empty.
func TestTarEmptyLinkSkipped(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()

	err := os.Symlink("target", filepath.Join(tmp, "symlink-probe"))
	if err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	archivePath := filepath.Join(tmp, "empty-link.tar.gz")
	extractDir := filepath.Join(tmp, "out")

	require.NoError(t, createEmptyLinkWithFileTarGzip(archivePath))

	_, files, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:  archivePath,
		OutputDir: extractDir,
		FileMode:  0o755,
		DirMode:   0o755,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"keep.txt"}, files)

	_, err = os.Lstat(filepath.Join(extractDir, "empty.link"))
	require.Error(t, err)
	assert.True(t, os.IsNotExist(err))

	data, err := os.ReadFile(filepath.Join(extractDir, "keep.txt"))
	require.NoError(t, err)
	assert.Equal(t, "kept", string(data))
}

// TestExtractTarGzipSquashRoot moves a single archive root up without going
// through ExtractFile. That path used to panic: only ExtractFile bound moveFiles.
func TestExtractTarGzipSquashRoot(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	archive := filepath.Join(dir, "one.tar.gz")
	require.NoError(t, writeGzipTarMember(archive, "pkg/a.txt", []byte("hi")))

	out := filepath.Join(dir, "out")
	_, files, err := xtractr.ExtractTarGzip(&xtractr.XFile{
		FilePath:   archive,
		OutputDir:  out,
		FileMode:   0o644,
		DirMode:    0o755,
		SquashRoot: true,
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(out, "a.txt"))

	_, err = os.Stat(filepath.Join(out, "pkg"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NotEmpty(t, files)
}

func writeGzipTarMember(dest, name string, payload []byte) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("create tar.gz: %w", err)
	}

	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	err = tarWriter.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	})
	if err != nil {
		return fmt.Errorf("tar header: %w", err)
	}

	_, err = tarWriter.Write(payload)
	if err != nil {
		return fmt.Errorf("tar payload: %w", err)
	}

	return nil
}

func mustFileSize(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)

	return info.Size()
}

func createSymlinkTarGzip(dest string) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	payload := []byte("shared-object-bytes")

	header := &tar.Header{
		Name:     "libfoo.so.1.2.3",
		Mode:     0o755,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	}

	err = tarWriter.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write tar header: %w", err)
	}

	_, err = tarWriter.Write(payload)
	if err != nil {
		return fmt.Errorf("failed to write tar payload: %w", err)
	}

	for _, link := range []struct {
		name   string
		target string
		kind   byte
	}{
		{"libfoo.so.1", "libfoo.so.1.2.3", tar.TypeSymlink},
		{"libfoo.so", "libfoo.so.1", tar.TypeSymlink},
		{"libfoo.hard", "libfoo.so.1.2.3", tar.TypeLink},
	} {
		header = &tar.Header{
			Name:     link.name,
			Linkname: link.target,
			Mode:     0o755,
			Typeflag: link.kind,
			ModTime:  time.Now(),
		}

		err = tarWriter.WriteHeader(header)
		if err != nil {
			return fmt.Errorf("failed to write link header: %w", err)
		}
	}

	return nil
}

func createLinkOnlyTarGzip(dest, name, linkName string, kind byte) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	header := &tar.Header{
		Name:     name,
		Linkname: linkName,
		Mode:     0o755,
		Typeflag: kind,
		ModTime:  time.Now(),
	}

	err = tarWriter.WriteHeader(header)
	if err != nil {
		return fmt.Errorf("failed to write link header: %w", err)
	}

	return nil
}

func createEmptyLinkWithFileTarGzip(dest string) error {
	archiveFile, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	defer archiveFile.Close()

	gzipWriter := gzip.NewWriter(archiveFile)
	defer gzipWriter.Close()

	tarWriter := tar.NewWriter(gzipWriter)
	defer tarWriter.Close()

	emptyLink := &tar.Header{
		Name:     "empty.link",
		Linkname: "",
		Mode:     0o755,
		Typeflag: tar.TypeSymlink,
		ModTime:  time.Now(),
	}

	err = tarWriter.WriteHeader(emptyLink)
	if err != nil {
		return fmt.Errorf("failed to write empty link header: %w", err)
	}

	payload := []byte("kept")
	fileHeader := &tar.Header{
		Name:     "keep.txt",
		Mode:     0o644,
		Size:     int64(len(payload)),
		Typeflag: tar.TypeReg,
		ModTime:  time.Now(),
	}

	err = tarWriter.WriteHeader(fileHeader)
	if err != nil {
		return fmt.Errorf("failed to write file header: %w", err)
	}

	_, err = tarWriter.Write(payload)
	if err != nil {
		return fmt.Errorf("failed to write file payload: %w", err)
	}

	return nil
}

func writeTar(sourceDir string, destWriter io.Writer) error {
	tarWriter := tar.NewWriter(destWriter)
	walkFS := os.DirFS(sourceDir)

	err := fs.WalkDir(walkFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			return nil
		}

		if path == "." {
			return nil
		}

		fileReader, err := walkFS.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open file: %w", err)
		}
		defer fileReader.Close()

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("failed to get file info: %w", err)
		}

		header, err := tar.FileInfoHeader(info, path)
		if err != nil {
			return fmt.Errorf("failed to create tar header: %w", err)
		}

		header.Name = filepath.ToSlash(path)

		err = tarWriter.WriteHeader(header)
		if err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		_, err = io.Copy(tarWriter, fileReader)
		if err != nil {
			return fmt.Errorf("failed to copy file (%s) into tar: %w", path, err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to walk source directory: %w", err)
	}

	return nil
}

func (c *tarCompressor) Compress(t *testing.T, sourceDir, destBase string) error {
	t.Helper()

	tarFile, err := os.Create(destBase + ".tar")

	require.NoError(t, err)
	defer safeCloser(t, tarFile)

	return writeTar(sourceDir, tarFile)
}

func (c *tarZCompressor) Compress(t *testing.T, _, destBase string) error {
	t.Helper()

	// No native Go library for .tar.Z and compress Unix utility is not available on
	// Windows. So, we use a pre-compressed file from test_data.
	tarZFilename := destBase + ".tar.z"
	tarZDestFile, err := os.Create(tarZFilename)

	require.NoError(t, err)
	defer safeCloser(t, tarZDestFile)

	tarZTestFile, err := os.Open("test_data/archive.tar.Z")

	require.NoError(t, err)
	defer safeCloser(t, tarZTestFile)

	written, err := io.Copy(tarZDestFile, tarZTestFile)
	assert.Positive(t, written)
	require.NoError(t, err)

	return nil
}

func (c *tarBzipCompressor) Compress(t *testing.T, sourceDir, destBase string) error {
	t.Helper()

	tarBZ2Filename := destBase + ".tar.bz2"

	tarBZ2File, err := os.Create(tarBZ2Filename)

	require.NoError(t, err)
	defer safeCloser(t, tarBZ2File)

	bzip2Writer, err := bzip2.NewWriter(tarBZ2File, &bzip2.WriterConfig{Level: bzip2.BestSpeed})

	require.NoError(t, err)
	defer safeCloser(t, bzip2Writer)

	err = writeTar(sourceDir, bzip2Writer)
	require.NoError(t, err)

	return nil
}

func (c *tarXZCompressor) Compress(t *testing.T, sourceDir, destBase string) error {
	t.Helper()

	tarXZFilename := destBase + ".tar.xz"

	tarXZFile, err := os.Create(tarXZFilename)

	require.NoError(t, err)
	defer safeCloser(t, tarXZFile)

	xzWriter, err := xz.NewWriter(tarXZFile)

	require.NoError(t, err)
	defer safeCloser(t, xzWriter)

	err = writeTar(sourceDir, xzWriter)
	require.NoError(t, err)

	return nil
}

func (c *tarGzipCompressor) Compress(t *testing.T, sourceDir, destBase string) error {
	t.Helper()

	tarGZFilename := destBase + ".tar.gz"

	tarGZFile, err := os.Create(tarGZFilename)

	require.NoError(t, err)
	defer safeCloser(t, tarGZFile)

	gzipWriter := gzip.NewWriter(tarGZFile)

	require.NoError(t, err)
	defer safeCloser(t, gzipWriter)

	err = writeTar(sourceDir, gzipWriter)
	require.NoError(t, err)

	return nil
}
