package xtractr

import (
	"path/filepath"
	"strings"
)

// CheckVolumes reads the headers of a multi-volume RAR or .7z.001 set and
// extracts nothing. It returns ErrMissingVolume when the set is not all on
// disk. A file that is not one of those sets returns nil. Any other header
// error is returned as extract would return it. Only FilePath, Password and
// Passwords are read.
func CheckVolumes(xFile *XFile) error {
	switch {
	case rarVolumeStart(xFile.FilePath):
		// Sizes belong to the ratio check. This read only wants the error.
		_, _, _, err := forRARPassword(xFile, checkRARVolumes)

		return err
	case strings.HasSuffix(strings.ToLower(xFile.FilePath), ".7z.001"):
		return sevenZipMissing(xFile.FilePath)
	default:
		return nil
	}
}

func rarVolumeStart(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".rar", ".r00":
		return true
	default:
		return false
	}
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
