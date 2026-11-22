package xtractr

/* How to extract a RAR file. */

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

const (
	rar4HeaderBytes     = 14
	rar4BlockPrefix     = 7
	rar4MainSize        = 13
	rar4MainType        = 0x73
	rar4FlagVolume      = 0x0001
	rar4FlagComment     = 0x0002
	rar4FlagNewNaming   = 0x0010
	rar4FlagFirstVolume = 0x0100
	rar4FlagBlockData   = 0x8000

	// rar4Marker is the RAR 1.5/4 signature. RAR5 uses 0x01 in the last byte.
	rar4Marker = "Rar!\x1a\x07\x00"
)

// ExtractRAR attempts to extract a file as a rar file.
func ExtractRAR(xFile *XFile) (size uint64, filesList, archiveList []string, err error) {
	if len(xFile.Passwords) == 0 && xFile.Password == "" {
		return extractRAR(xFile)
	}

	// Try all the passwords.
	passwords := xFile.Passwords

	if xFile.Password != "" { // If a single password is provided, try it first.
		passwords = append([]string{xFile.Password}, xFile.Passwords...)
	}

	for idx, password := range passwords {
		size, files, archives, err := tryRAR(xFile, password)

		switch {
		case err == nil:
			return size, files, archives, nil
		case IsLimitError(err):
			return size, files, archives, err
		case strings.Contains(err.Error(), "incorrect password"):
			// https://github.com/nwaples/rardecode/issues/28
			continue
		default:
			return size, files, archives, fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
		}
	}

	// No password worked, try without a password. tryRAR copies the attempt
	// tracker back so a later signature fallback keeps the same budget.
	return tryRAR(xFile, "")
}

// tryRAR runs one extractRAR attempt on a copy (so Password can change) and
// writes the attempt's progress tracker back onto xFile.
func tryRAR(xFile *XFile, password string) (uint64, []string, []string, error) {
	attempt := *xFile
	attempt.Password = password

	size, files, archives, err := extractRAR(&attempt)
	xFile.prog = attempt.prog

	return size, files, archives, err
}

// extractRAR extracts a rar file. to a destination. This wraps github.com/nwaples/rardecode.
func extractRAR(xFile *XFile) (uint64, []string, []string, error) {
	total, compressed, count, err := rarSetHeaders(xFile)
	if err != nil {
		return 0, nil, nil, err
	}

	tracker, headerErr := xFile.archiveProgress(total, compressed, count)
	defer tracker.done()

	if headerErr != nil {
		return 0, nil, nil, headerErr
	}

	rarReader, err := rardecode.OpenReader(xFile.FilePath, rardecode.Password(xFile.Password)) // open it again.
	if err != nil {
		return 0, nil, nil, fmt.Errorf("rardecode.OpenReader: %w", err)
	}
	defer rarReader.Close()

	files, err := xFile.unrar(rarReader)
	if err != nil {
		volumes := normalizeVolumes(rarReader.Volumes(), xFile.FilePath)
		lastFile := volumes[len(volumes)-1]

		return xFile.prog.Wrote, files, volumes, fmt.Errorf("%s: %w", lastFile, err)
	}

	return xFile.prog.Wrote, files, normalizeVolumes(rarReader.Volumes(), xFile.FilePath), nil
}

// rarSetHeaders reads the headers of the RAR set that starts at xFile.FilePath
// and extracts nothing. It returns ErrMissingVolume when the set is not all
// on disk. total, compressed and count are what the ratio check needs, and
// they are zero when err is not nil. Only FilePath and Password are read.
func rarSetHeaders(xFile *XFile) (total, compressed uint64, count int, err error) {
	if rar4LaterVolume(xFile.FilePath) {
		return 0, 0, 0, fmt.Errorf("%w: %s", ErrMissingVolume, xFile.FilePath)
	}

	rarReader, err := rardecode.OpenReader(xFile.FilePath, rardecode.Password(xFile.Password))
	if err != nil {
		if errors.Is(err, rardecode.ErrBadVolumeNumber) {
			// A RAR5 main header carries its volume number, and the first open expects 0.
			return 0, 0, 0, fmt.Errorf("%w: %w", ErrMissingVolume, err)
		}

		return 0, 0, 0, fmt.Errorf("rardecode.OpenReader: %w", err)
	}

	return getUncompressedRarSize(rarReader, xFile) // closes rarReader
}

// rar4LaterVolume reports whether path is a RAR4 volume that follows another
// volume. rardecode does not read the first-volume flag, so a later volume
// opened on its own can look like a whole archive. A read error, a bad main
// header CRC, or any other signature returns false, and that file is left to
// the normal open.
func rar4LaterVolume(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	buf := make([]byte, rar4HeaderBytes)
	_, err = io.ReadFull(file, buf)

	if err != nil || !bytes.Equal(buf[:len(rar4Marker)], []byte(rar4Marker)) || buf[9] != rar4MainType {
		return false
	}

	flags := binary.LittleEndian.Uint16(buf[10:12])
	if flags&rar4FlagVolume == 0 || flags&rar4FlagFirstVolume != 0 {
		return false
	}

	// RAR 2.x set the volume flag and never the first-volume flag, and it
	// named a first volume .rar. RAR 3.0 and later set the first-volume flag.
	if flags&rar4FlagNewNaming == 0 && strings.EqualFold(filepath.Ext(path), ".rar") {
		return false
	}

	return rar4MainCRC(file, buf)
}

// rar4MainCRC reports whether buf's main header matches its CRC. The flags are
// trusted only after this, so a corrupt first volume is not a missing one.
// file is positioned just after the 14 bytes in buf.
func rar4MainCRC(file *os.File, buf []byte) bool {
	flags := binary.LittleEndian.Uint16(buf[10:12])
	if flags&rar4FlagBlockData != 0 {
		return false
	}

	size := int(binary.LittleEndian.Uint16(buf[12:14]))
	if flags&rar4FlagComment != 0 {
		if size < rar4MainSize {
			return false
		}

		size = rar4MainSize
	} else if size < rar4BlockPrefix {
		return false
	}

	extra := make([]byte, size-rar4BlockPrefix)

	_, err := io.ReadFull(file, extra)
	if err != nil {
		return false
	}

	sum := crc32.NewIEEE()
	_, _ = sum.Write(buf[9:rar4HeaderBytes])
	_, _ = sum.Write(extra)

	// The header stores the low 16 bits of the CRC32.
	stored := uint32(binary.LittleEndian.Uint16(buf[7:9]))

	return sum.Sum32()&0xffff == stored
}

// getUncompressedRarSize reads every file header, which walks every volume.
// Only io.EOF ends the archive. Any other error is returned: summing the
// volumes read so far would compare the claimed size against part of the
// archive and could report a false compression ratio cap hit.
func getUncompressedRarSize(
	rarReader *rardecode.ReadCloser, xFile *XFile,
) (total, compressed uint64, count int, err error) {
	defer rarReader.Close()

	for {
		header, nextErr := rarReader.Next()
		if nextErr != nil {
			volumes := normalizeVolumes(rarReader.Volumes(), xFile.FilePath)
			// Before the header ratio check. Sibling volumes are not FilePath.
			xFile.omitNoted(volumes...)
			compressed = archiveFileSizes(volumes...)

			return total, compressed, count, rarHeaderError(nextErr, count)
		}

		total += uint64(header.UnPackedSize)
		count++
	}
}

// rarHeaderError is nil at the end of the archive. A set that is not all on
// disk becomes ErrMissingVolume: the next volume is not there, a file runs
// past the last volume, or the first header continues a file from a volume
// that was not opened.
func rarHeaderError(err error, count int) error {
	switch {
	case errors.Is(err, io.EOF):
		return nil
	case errors.Is(err, fs.ErrNotExist), // the next volume is not on disk
		errors.Is(err, rardecode.ErrUnexpectedArcEnd),               // a file runs past the last volume
		count == 0 && errors.Is(err, rardecode.ErrInvalidFileBlock): // the set starts in an earlier volume
		return fmt.Errorf("%w: %w", ErrMissingVolume, err)
	default:
		return fmt.Errorf("rarReader.Next: %w", err)
	}
}

func (x *XFile) unrar(rarReader *rardecode.ReadCloser) ([]string, error) {
	files := []string{}

	for {
		header, err := rarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return files, fmt.Errorf("rarReader.Next: %w", err)
		}

		file := &file{
			Path:     x.clean(header.Name),
			Data:     rarReader,
			FileMode: header.Mode(),
			Mtime:    header.ModificationTime,
			Atime:    header.AccessTime,
			Linkname: header.LinkTarget,
		}

		// RAR5 stores symlink targets in a redirection record (not file payload).
		// Mode() already flags unix/windows symlinks; junctions still need ModeSymlink.
		switch header.LinkType {
		case rardecode.LinkTypeUnixSymlink, rardecode.LinkTypeWindowsSymlink, rardecode.LinkTypeWindowsJunction:
			file.FileMode |= os.ModeSymlink
		}

		if !x.pathWithinOutput(file.Path) {
			// The file being written is trying to write outside of our base path. Malicious archive?
			return files, fmt.Errorf("%s: %w: %s != %s (from: %s)",
				x.FilePath, ErrInvalidPath, file.Path, x.OutputDir, header.Name)
		}

		if header.IsDir {
			x.Debugf("Writing archived directory: %s", file.Path)

			err = x.mkDir(file.Path, header.ModificationTime)
			if err != nil {
				return files, fmt.Errorf("making rar file dir: %w", err)
			}

			continue
		}

		x.Debugf("Writing archived file: %s (packed: %d, unpacked: %d)",
			file.Path, header.PackedSize, header.UnPackedSize)

		fSize, err := x.write(file)
		if err != nil {
			return files, err
		}

		files = append(files, file.Path)
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			file.Path, fSize, x.prog.Files, x.prog.Wrote)
	}

	return x.cleanup(files)
}
