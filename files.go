package xtractr

/* Code to find, write, move and delete files. */

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// XFile defines the data needed to extract an archive.
type XFile struct {
	FilePath  string      // Path to archive being extracted.
	OutputDir string      // Folder to extract archive into.
	FileMode  os.FileMode // Write files with this mode.
	DirMode   os.FileMode // Write folders with this mode.
	Password  string      // (RAR) Archive password. Blank for none.
	Passwords []string    // (RAR) Archive passwords (to try multiple).
}

// GetFileList returns all the files in a path.
// This is non-resursive and only returns files _in_ the base path provided.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) GetFileList(path string) (files []string) {
	if fileList, err := ioutil.ReadDir(path); err == nil {
		for _, file := range fileList {
			files = append(files, filepath.Join(path, file.Name()))
		}
	} else {
		x.config.Printf("Error: Reading path '%s': %v", path, err)
	}

	return
}

// Difference returns all the strings that are in slice2 but not in slice1.
// Used to find new files in a file list from a path. ie. those we extracted.
// This is a helper method and only exposed for convenience. You do not have to call this.
func Difference(slice1 []string, slice2 []string) (diff []string) {
	for _, s2p := range slice2 {
		var found bool

		for _, s1 := range slice1 {
			if s1 == s2p {
				found = true

				break
			}
		}

		if !found { // String not found, so it's a new string, add it to the diff.
			diff = append(diff, s2p)
		}
	}

	return diff
}

// FindCompressedFiles returns all the rar and zip files in a path. This attempts to grab
// only the first file in a multi-part archive. Sometimes there are multiple archives, so
// if the archive does not have "part" followed by a number in the name, then it will be
// considered an independent archive. Some packagers seem to use different naming schemes,
// so this will need to be updated as time progresses. So far it's working well.
// This is a helper method and only exposed for convenience. You do not have to call this.
func FindCompressedFiles(path string) []string {
	dir, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer dir.Close()

	if info, err := dir.Stat(); err != nil {
		return nil // unreadable folder?
	} else if l := strings.ToLower(path); !info.IsDir() &&
		(strings.HasSuffix(l, ".zip") || strings.HasSuffix(l, ".rar") || strings.HasSuffix(l, ".r00")) {
		return []string{path} // passed in an archive file; send it back out.
	}

	fileList, err := dir.Readdir(-1)
	if err != nil {
		return nil
	}

	// Check (save) if the current path has any rar files.
	// So we can ignore r00 if it does.
	r, _ := filepath.Glob(filepath.Join(path, "*.rar"))

	return getCompressedFiles(len(r) > 0, path, fileList)
}

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles(hasrar bool, path string, fileList []os.FileInfo) []string { //nolint:cyclop
	files := []string{}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case lowerName == "" || lowerName[0] == '.':
			continue // ignore empty names and dot files/folders.
		case file.IsDir(): // Recurse.
			files = append(files, FindCompressedFiles(filepath.Join(path, file.Name()))...)
		case strings.HasSuffix(lowerName, ".zip") || strings.HasSuffix(lowerName, ".tar") ||
			strings.HasSuffix(lowerName, ".tgz") || strings.HasSuffix(lowerName, ".gz") ||
			strings.HasSuffix(lowerName, ".bz2") || strings.HasSuffix(lowerName, ".7z"):
			files = append(files, filepath.Join(path, file.Name()))
		case strings.HasSuffix(lowerName, ".rar"):
			hasParts := regexp.MustCompile(`.*\.part[0-9]+\.rar$`)
			partOne := regexp.MustCompile(`.*\.part0*1\.rar$`)
			// Some archives are named poorly. Only return part01 or part001, not all.
			if !hasParts.Match([]byte(lowerName)) || partOne.Match([]byte(lowerName)) {
				files = append(files, filepath.Join(path, file.Name()))
			}
		case !hasrar && strings.HasSuffix(lowerName, ".r00"):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			files = append(files, filepath.Join(path, file.Name()))
		}
	}

	return files
}

// Extract calls the correct procedure for the type of file being extracted.
// Returns size of extracted data, list of extracted files, and/or error.
func (x *XFile) Extract() (int64, []string, []string, error) {
	return ExtractFile(x)
}

// ExtractFile calls the correct procedure for the type of file being extracted.
// Returns size of extracted data, list of extracted files, list of archives processed, and/or error.
func ExtractFile(xFile *XFile) (int64, []string, []string, error) {
	var (
		size  int64
		files []string
		err   error
	)

	switch sName := strings.ToLower(xFile.FilePath); {
	case strings.HasSuffix(sName, ".rar"), strings.HasSuffix(sName, ".r00"):
		return ExtractRAR(xFile)
	case strings.HasSuffix(sName, ".7z"):
		size, files, err = Extract7z(xFile)
	case strings.HasSuffix(sName, ".zip"):
		size, files, err = ExtractZIP(xFile)
	case strings.HasSuffix(sName, ".tar.gz"), strings.HasSuffix(sName, ".tgz"):
		size, files, err = ExtractTarGzip(xFile)
	case strings.HasSuffix(sName, ".tar.bz2"), strings.HasSuffix(sName, ".tbz2"),
		strings.HasSuffix(sName, ".tbz"), strings.HasSuffix(sName, ".tar.bz"):
		size, files, err = ExtractTarBzip(xFile)
	case strings.HasSuffix(sName, ".bz"), strings.HasSuffix(sName, ".bz2"):
		size, files, err = ExtractBzip(xFile)
	case strings.HasSuffix(sName, ".gz"):
		size, files, err = ExtractGzip(xFile)
	case strings.HasSuffix(sName, ".tar"):
		size, files, err = ExtractTar(xFile)
	default:
		return 0, nil, nil, fmt.Errorf("%w: %s", ErrUnknownArchiveType, xFile.FilePath)
	}

	return size, files, []string{xFile.FilePath}, err
}

// MoveFiles relocates files then removes the folder they were in.
// Returns the new file paths.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) MoveFiles(fromPath string, toPath string, overwrite bool) ([]string, error) {
	var (
		files    = x.GetFileList(fromPath)
		newFiles = []string{}
		keepErr  error
	)

	if err := os.MkdirAll(toPath, x.config.DirMode); err != nil {
		return nil, fmt.Errorf("os.MkDirAll: %w", err)
	}

	for _, file := range files {
		var (
			newFile = filepath.Join(toPath, filepath.Base(file))
			_, err  = os.Stat(newFile)
			exists  = !os.IsNotExist(err)
		)

		if exists && !overwrite {
			x.config.Printf("Error: Renaming Temp File: %v to %v: (refusing to overwrite existing file)", file, newFile)
			// keep trying.
			continue
		}

		switch err = x.Rename(file, newFile); {
		case err != nil:
			keepErr = err
			x.config.Printf("Error: Renaming Temp File: %v to %v: %v", file, newFile, err)
		case exists:
			newFiles = append(newFiles, newFile)
			x.config.Debugf("Renamed Temp File: %v -> %v (overwrote existing file)", file, newFile)
		default:
			newFiles = append(newFiles, newFile)
			x.config.Debugf("Renamed Temp File: %v -> %v", file, newFile)
		}
	}

	x.DeleteFiles(fromPath)
	// Since this is the last step, we tried to rename all the files, bubble the
	// os.Rename error up, so it gets flagged as failed. It may have worked, but
	// it should get attention.
	return newFiles, keepErr
}

// DeleteFiles obliterates things and logs. Use with caution.
func (x *Xtractr) DeleteFiles(files ...string) {
	for _, file := range files {
		if err := os.RemoveAll(file); err != nil {
			x.config.Printf("Error: Deleting %v: %v", file, err)

			continue
		}

		x.config.Printf("Deleted (recursively): %s", file)
	}
}

// writeFile writes a file from an io reader, making sure all parent directories exist.
func writeFile(fpath string, fdata io.Reader, fMode, dMode os.FileMode) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(fpath), dMode); err != nil {
		return 0, fmt.Errorf("os.MkdirAll: %w", err)
	}

	fout, err := os.Create(fpath)
	if err != nil {
		return 0, fmt.Errorf("os.Create: %w", err)
	}
	defer fout.Close()

	if runtime.GOOS != "windows" {
		if err = fout.Chmod(fMode); err != nil {
			return 0, fmt.Errorf("chmod: %w", err)
		}
	}

	s, err := io.Copy(fout, fdata)
	if err != nil {
		return s, fmt.Errorf("copying io: %w", err)
	}

	return s, nil
}

// Rename is an attempt to deal with "invalid cross link device" on weird file systems.
func (x *Xtractr) Rename(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err == nil {
		return nil
	}

	/* Rename failed, try copy. */

	oldFile, err := os.Open(oldpath) // do not forget to close this!
	if err != nil {
		return fmt.Errorf("os.Open(): %w", err)
	}

	newFile, err := os.OpenFile(newpath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, x.config.FileMode)
	if err != nil {
		oldFile.Close()
		return fmt.Errorf("os.OpenFile(): %w", err)
	}
	defer newFile.Close()

	_, err = io.Copy(newFile, oldFile)
	oldFile.Close()

	if err != nil {
		return fmt.Errorf("io.Copy(): %w", err)
	}

	// The copy was successful, so now delete the original file
	_ = os.Remove(oldpath)

	return nil
}

// clean returns an absolute path for a file inside the OutputDir.
// If trim length is > 0, then the suffixes are trimmed, and filepath removed.
func (x *XFile) clean(filePath string, trim ...string) string {
	if len(trim) != 0 {
		filePath = filepath.Base(filePath)
		for _, suffix := range trim {
			filePath = strings.TrimSuffix(filePath, suffix)
		}
	}

	return filepath.Clean(filepath.Join(x.OutputDir, filePath))
}
