package xtractr_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwaples/rardecode/v2"
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

// TestExtractRARCorruptHeader checks that a header failure other than a
// missing volume keeps the original error, writes nothing, and is not
// reported as a missing volume or a compression ratio cap hit, whether or not
// MaxRatio is set. The header scan used to swallow every error, so a damaged
// header could fall through to a misleading ratio check or a partial extract.
func TestExtractRARCorruptHeader(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("test_data", "multivol.part1.rar"))
	require.NoError(t, err)

	// Flip a byte inside the first file header, after the signature and main
	// header, so the archive opens but reading the header fails its checksum.
	const headerByte = 0x20

	data[headerByte] ^= 0xff

	for name, maxRatio := range map[string]float64{"no ratio cap": 0, "ratio cap": 1} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "corrupt.rar")
			require.NoError(t, os.WriteFile(path, data, 0o600)) //nolint:gosec // fixed name in a temp dir.

			xFile := rarRatioFile(t, path)
			xFile.MaxRatio = maxRatio

			size, files, archives, err := xtractr.ExtractRAR(xFile)
			require.Error(t, err)
			require.NotErrorIs(t, err, xtractr.ErrMissingVolume, "a damaged header is not a missing volume")
			require.NotErrorIs(t, err, fs.ErrNotExist)
			assert.False(t, xtractr.IsLimitError(err), "a damaged header is not a configured limit: %v", err)
			require.ErrorContains(t, err, "rarReader.Next", "the header read error must be kept")
			assert.Zero(t, size)
			assert.Empty(t, files)
			assert.Empty(t, archives)

			written, readErr := os.ReadDir(xFile.OutputDir)
			require.NoError(t, readErr)
			assert.Empty(t, written, "nothing may be extracted from a damaged archive")
		})
	}
}

// TestExtractRAR4MissingVolume covers RAR4 sets rardecode does not report as a
// missing volume on its own: a later volume opened in place of the first, a
// file that runs past the last volume when the volume has no end block, and a
// RAR5 volume whose number is not zero.
func TestExtractRAR4MissingVolume(t *testing.T) {
	t.Parallel()

	payload := bytes.Repeat([]byte("rar4-volume-"), 20)

	t.Run("continued file", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 16}, rar4File{name: "payload.bin", data: payload})
		require.NoError(t, os.Remove(filepath.Join(dir, names[0])))

		assertRARMissing(t, filepath.Join(dir, names[1]), nil)
	})

	t.Run("second file starts at r00", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: len(payload)},
			rar4File{name: "first.bin", data: payload},
			rar4File{name: "second.bin", data: payload},
		)
		require.NoError(t, os.Remove(filepath.Join(dir, names[0])))

		assertRARMissing(t, filepath.Join(dir, names[1]), nil)
	})

	t.Run("no end block", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		names := writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 16, noEnd: true},
			rar4File{name: "payload.bin", data: payload})
		require.NoError(t, os.Remove(filepath.Join(dir, names[len(names)-1])))

		assertRARMissing(t, filepath.Join(dir, names[0]), nil)
	})

	t.Run("rar5 volume number", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()

		for idx, src := range []string{"multivol.part2.rar", "multivol.part3.rar", "multivol.part4.rar"} {
			data, err := os.ReadFile(filepath.Join("test_data", src))
			require.NoError(t, err)

			dest := filepath.Join(dir, fmt.Sprintf("multivol.r%02d", idx))
			require.NoError(t, os.WriteFile(dest, data, 0o600)) //nolint:gosec // fixed name in a temp dir.
		}

		assertRARMissing(t, filepath.Join(dir, "multivol.r00"), rardecode.ErrBadVolumeNumber)
	})

	// A .rar with the volume flag and neither the first-volume nor the new-numbering
	// flag is what RAR 2.x used for a first volume, so the flag check leaves it
	// to the header walk. A continued file there is still a missing volume.
	t.Run("continued rar without first flag", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.rar", rar4Options{}, rar4File{name: "payload.bin", data: payload, continued: true})

		assertRARMissing(t, filepath.Join(dir, "show.rar"), rardecode.ErrInvalidFileBlock)
	})
}

// TestExtractRAR4FirstVolumeStillExtracts checks the two first volumes the
// later-volume flag check must not reject: one named .r00 that carries the
// first-volume flag, and a .rar with the volume flag and no first-volume flag.
func TestExtractRAR4FirstVolumeStillExtracts(t *testing.T) {
	t.Parallel()

	payload := []byte("rar4 first volume still extracts")

	t.Run("r00 with first flag", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.r00", rar4Options{volSize: 10, firstVolume: true},
			rar4File{name: "payload.bin", data: payload})
		assertRARExtracted(t, filepath.Join(dir, "show.r00"), "payload.bin", payload)
	})

	t.Run("rar without first flag", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeRAR4Set(t, dir, "show.rar", rar4Options{volSize: 10}, rar4File{name: "payload.bin", data: payload})
		assertRARExtracted(t, filepath.Join(dir, "show.rar"), "payload.bin", payload)
	})
}

func assertRARMissing(t *testing.T, path string, wrapped error) {
	t.Helper()

	xFile := rarRatioFile(t, path)
	size, files, archives, err := xtractr.ExtractRAR(xFile)
	require.ErrorIs(t, err, xtractr.ErrMissingVolume)

	if wrapped != nil {
		require.ErrorIs(t, err, wrapped)
	}

	assert.False(t, xtractr.IsLimitError(err), "a missing volume is not a configured limit: %v", err)
	assert.Zero(t, size)
	assert.Empty(t, files)
	assert.Empty(t, archives)

	written, readErr := os.ReadDir(xFile.OutputDir)
	require.NoError(t, readErr)
	assert.Empty(t, written)
}

func assertRARExtracted(t *testing.T, path, name string, payload []byte) {
	t.Helper()

	xFile := rarRatioFile(t, path)
	xFile.MaxRatio = 0

	size, files, _, err := xtractr.ExtractRAR(xFile)
	require.NoError(t, err)
	assert.Equal(t, uint64(len(payload)), size)
	require.Len(t, files, 1)

	got, err := os.ReadFile(files[0])
	require.NoError(t, err)
	assert.Equal(t, payload, got)
	assert.Equal(t, name, filepath.Base(files[0]))
}

const (
	rar4MainType        = 0x73
	rar4FlagVolume      = 0x0001
	rar4FlagFirstVolume = 0x0100

	// rar4Marker is the RAR 1.5/4 signature.
	rar4Marker = "Rar!\x1a\x07\x00"
)

type rar4File struct {
	name      string
	data      []byte
	continued bool // this file starts in a volume that is not written
}

type rar4Options struct {
	volSize     int
	firstVolume bool
	noEnd       bool
}

type rar4Piece struct {
	name  string
	data  []byte
	whole []byte
	cont  bool
	more  bool
}

// writeRAR4Set writes a store-mode RAR 1.5 set and returns the volume names.
// volSize is the file bytes per volume; zero keeps every file in one volume.
func writeRAR4Set(t *testing.T, dir, firstName string, opts rar4Options, files ...rar4File) []string {
	t.Helper()

	volumes := splitRAR4(files, opts.volSize)
	require.NotEmpty(t, volumes)

	names := make([]string, len(volumes))
	name := firstName

	for idx, pieces := range volumes {
		names[idx] = name

		var body []byte

		body = append(body, rar4Marker...)
		body = append(body, rar4MainHeader(idx == 0 && opts.firstVolume)...)

		for _, piece := range pieces {
			body = append(body, rar4FileHeader(piece)...)
			body = append(body, piece.data...)
		}

		if !opts.noEnd {
			notLast := idx < len(volumes)-1
			body = append(body, rar4EndHeader(notLast)...)
		}

		require.NoError(t, os.WriteFile(filepath.Join(dir, name), body, 0o600))

		name = nextOldRARVolume(name)
	}

	return names
}

func splitRAR4(files []rar4File, volSize int) [][]rar4Piece {
	room := volSize
	if volSize <= 0 {
		room = int(^uint(0) >> 1)
	}

	var (
		volumes [][]rar4Piece
		cur     []rar4Piece
	)

	flush := func() {
		if len(cur) == 0 {
			return
		}

		volumes = append(volumes, cur)
		cur = nil
		room = volSize

		if volSize <= 0 {
			room = int(^uint(0) >> 1)
		}
	}

	for _, file := range files {
		for off := 0; off < len(file.data); {
			take := min(len(file.data)-off, room)
			cur = append(cur, rar4Piece{
				name:  file.name,
				data:  file.data[off : off+take],
				whole: file.data,
				cont:  file.continued || off > 0,
				more:  off+take < len(file.data),
			})
			off += take
			room -= take

			if room == 0 {
				flush()
			}
		}
	}

	flush()

	return volumes
}

func rar4MainHeader(first bool) []byte {
	flags := uint16(rar4FlagVolume)
	if first {
		flags |= rar4FlagFirstVolume
	}

	body := make([]byte, 11)
	body[0] = rar4MainType
	binary.LittleEndian.PutUint16(body[1:3], flags)
	binary.LittleEndian.PutUint16(body[3:5], 13)

	return rar4Block(body)
}

func rar4FileHeader(piece rar4Piece) []byte {
	const (
		fileType       = 0x74
		blockHasData   = 0x8000
		splitBefore    = 0x0001
		splitAfter     = 0x0002
		storedMethod   = 0x30
		decoderVer     = 20
		unixHost       = 3 // rardecode adds one, which is HostOSUnix
		headerMinusCRC = 30
	)

	flags := uint16(blockHasData)
	if piece.cont {
		flags |= splitBefore
	}

	if piece.more {
		flags |= splitAfter
	}

	// rardecode compares the checksum of the whole file with the CRC on the
	// last piece. A piece that continues carries the CRC of its own bytes.
	sum := crc32.ChecksumIEEE(piece.data)
	if !piece.more {
		sum = crc32.ChecksumIEEE(piece.whole)
	}

	name := []byte(piece.name)
	body := make([]byte, headerMinusCRC+len(name))
	body[0] = fileType
	binary.LittleEndian.PutUint16(body[1:3], flags)
	binary.LittleEndian.PutUint16(body[3:5], uint16(headerMinusCRC+2+len(name)))
	binary.LittleEndian.PutUint32(body[5:9], uint32(len(piece.data)))
	binary.LittleEndian.PutUint32(body[9:13], uint32(len(piece.whole)))
	body[13] = unixHost
	binary.LittleEndian.PutUint32(body[14:18], sum)
	body[22] = decoderVer
	body[23] = storedMethod
	binary.LittleEndian.PutUint16(body[24:26], uint16(len(name)))
	binary.LittleEndian.PutUint32(body[26:30], 0o644)
	copy(body[30:], name)

	return rar4Block(body)
}

func rar4EndHeader(notLast bool) []byte {
	const endType = 0x7b

	flags := uint16(0)
	if notLast {
		flags = 0x0001
	}

	body := make([]byte, 5)
	body[0] = endType
	binary.LittleEndian.PutUint16(body[1:3], flags)
	binary.LittleEndian.PutUint16(body[3:5], 7)

	return rar4Block(body)
}

func rar4Block(body []byte) []byte {
	out := make([]byte, 2+len(body))
	binary.LittleEndian.PutUint16(out[:2], uint16(crc32.ChecksumIEEE(body)))
	copy(out[2:], body)

	return out
}

func nextOldRARVolume(name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	raw := []byte(strings.TrimPrefix(ext, "."))

	if len(raw) < 3 || raw[1] < '0' || raw[1] > '9' || raw[2] < '0' || raw[2] > '9' {
		return base + ".r00"
	}

	for idx := 2; idx >= 0; idx-- {
		if raw[idx] != '9' {
			raw[idx]++

			break
		}

		if idx == 0 {
			raw[idx] = 'A'
		} else {
			raw[idx] = '0'
		}
	}

	return base + "." + string(raw)
}
