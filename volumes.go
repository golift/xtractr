package xtractr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"
)

// CheckVolumes reads the headers of a multi-volume RAR or 7-Zip set and
// extracts nothing. A RAR is .rar or .r00 through .r99. A 7-Zip set is
// .7z.001 and the volumes after it. It returns ErrMissingVolume when the
// set is not all on disk, including a later volume whose first volume is
// absent. A file that is not one of those sets returns nil. Any other
// header error is returned as extract would return it. Only FilePath,
// Password and Passwords are read.
func CheckVolumes(xFile *XFile) error {
	switch {
	case rarVolumeStart(xFile.FilePath):
		// Sizes belong to the ratio check. This read only wants the error.
		_, _, _, err := forRARPassword(xFile, checkRARVolumes)

		return err
	default:
		return check7zVolumes(xFile)
	}
}

func rarVolumeStart(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".rar" {
		return true
	}

	// Old-style continuation volumes are .r00 through .r99.
	if len(ext) != len(".r00") || ext[1] != 'r' {
		return false
	}

	return ext[2] >= '0' && ext[2] <= '9' && ext[3] >= '0' && ext[3] <= '9'
}

// checkRARVolumes reads one password's headers. The copy keeps Password local
// to the attempt, and the tracker is written back like tryRAR.
func checkRARVolumes(xFile *XFile, password string) (uint64, []string, []string, error) {
	attempt := *xFile
	attempt.Password = password

	// Sizes belong to the ratio check. This read only wants the error.
	_, _, _, err := rarSetHeaders(&attempt) //nolint:dogsled
	xFile.prog = attempt.prog

	return 0, nil, nil, err
}

// check7zVolumes reports a short .7z.NNN set. A later volume is judged from
// its .001, which is where the start header lives. The header is then opened
// and closed so a corrupt set is not reported as complete.
func check7zVolumes(xFile *XFile) error {
	first, ok := sevenZipFirst(xFile.FilePath)
	if !ok {
		return nil
	}

	err := missing7zFirst(xFile.FilePath, first)
	if err != nil {
		return err
	}

	err = sevenZipMissing(first)
	if err != nil {
		return err
	}

	attempt := *xFile
	attempt.FilePath = first

	return open7zHeaders(&attempt)
}

// sevenZipFirst returns the .001 path for a .7z.NNN volume.
func sevenZipFirst(path string) (string, bool) {
	ext := filepath.Ext(path)
	if len(ext) != len(".001") || !digits(ext[1:]) {
		return "", false
	}

	stem := strings.TrimSuffix(path, ext)
	if !strings.HasSuffix(strings.ToLower(stem), ".7z") {
		return "", false
	}

	return stem + ".001", true
}

func digits(text string) bool {
	if text == "" {
		return false
	}

	for _, char := range text {
		if char < '0' || char > '9' {
			return false
		}
	}

	return true
}

func missing7zFirst(path, first string) error {
	if strings.EqualFold(path, first) {
		return nil
	}

	_, err := os.Stat(first)
	if err == nil {
		return nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrMissingVolume, first)
	}

	return fmt.Errorf("%s: %w", first, err)
}

// open7zHeaders opens the archive and closes it. Password then Passwords are
// tried in the same order as Extract7z. Nothing is written.
func open7zHeaders(xFile *XFile) error {
	passwords := xFile.Passwords

	if xFile.Password != "" {
		passwords = append([]string{xFile.Password}, xFile.Passwords...)
	}

	if len(passwords) == 0 {
		return open7zHeader(xFile.FilePath, "")
	}

	var err error

	for idx, password := range passwords {
		err = open7zHeader(xFile.FilePath, password)
		if err == nil {
			return nil
		}

		if idx == len(passwords)-1 {
			return fmt.Errorf("used password %d of %d: %w", idx+1, len(passwords), err)
		}
	}

	// unreachable code
	return err
}

func open7zHeader(path, password string) error {
	reader, err := sevenzip.OpenReaderWithPassword(path, password)
	if err != nil {
		return fmt.Errorf("%s: os.Open: %w", path, err)
	}

	err = reader.Close()
	if err != nil {
		return fmt.Errorf("%s: close: %w", path, err)
	}

	return nil
}
