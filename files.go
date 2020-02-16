package extractorr

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// GetFileList returns all the files in a path.
// This is non-resursive and only returns files _in_ the base path provided.
func (e *Extractorr) GetFileList(path string) (files []string) {
	if fileList, err := ioutil.ReadDir(path); err == nil {
		for _, file := range fileList {
			files = append(files, filepath.Join(path, file.Name()))
		}
	} else {
		e.log("Error: Reading path '%s': %v", path, err)
	}

	return
}

// Difference returns all the strings that are in slice2 but not in slice1.
// Used to find new files in a file list from a path. ie. those we extracted.
func Difference(slice1 []string, slice2 []string) (diff []string) {
	for _, s2 := range slice2 {
		var found bool

		for _, s1 := range slice1 {
			if s1 == s2 {
				found = true
				break
			}
		}

		if !found {
			// String not found.
			diff = append(diff, s2)
		}
	}

	return diff
}

// FindCompressedFiles returns all the rar and zip files in a path. This attempts to grab only the first
// file in a multi-part archive. Sometimes there are multiple archives, so if the archive
// does not have "part" followed by a number in the name, then it will be considered
// an independent archive. Some packagers seem to use different naming schemes, so this
// will need to be updated as time progresses. So far it's working well. -dn2@8/3/19
func FindCompressedFiles(path string) []string {
	fileList, err := ioutil.ReadDir(path)
	if err != nil {
		return nil
	}

	hasrar := false
	files := []string{}

	// Check (save) if the current path has any rar files.
	if r, err := filepath.Glob(filepath.Join(path, "*.rar")); err == nil && len(r) > 0 {
		hasrar = true
	}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case file.IsDir(): // Recurse.
			files = append(files, FindCompressedFiles(filepath.Join(path, file.Name()))...)
		case strings.HasSuffix(lowerName, ".zip"):
			files = append(files, filepath.Join(path, file.Name()))
		case strings.HasSuffix(lowerName, ".rar"):
			// Some archives are named poorly. Only return part01 or part001, not all.
			m, _ := filepath.Match("*.part[0-9]*.rar", lowerName)
			// This if statements says:
			// If the current file does not have "part0-9" in the name, add it to our list (all .rar files).
			// If it does have "part0-9" in the name, then make sure it's part 1.
			if !m || strings.HasSuffix(lowerName, ".part01.rar") ||
				strings.HasSuffix(lowerName, ".part001.rar") ||
				strings.HasSuffix(lowerName, ".part1.rar") {
				files = append(files, filepath.Join(path, file.Name()))
			}
		case !hasrar && strings.HasSuffix(lowerName, ".r00"):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			files = append(files, filepath.Join(path, file.Name()))
		}
	}

	return files
}

// ExtractFile calls the correct procedure for the type of file being extracted.
// Returns size of extracted data, number of extracted files, and/or error.
func ExtractFile(path, destination string) (int64, []string, error) {
	switch s := strings.ToLower(path); {
	case strings.HasSuffix(s, ".rar"):
		return ExtractRAR(path, destination)
	case strings.HasSuffix(s, ".zip"):
		return ExtractZIP(path, destination)
	}

	return 0, nil, fmt.Errorf("unknown filetype: %s", path)
}

// MoveFiles relocates files then removes the folder they were in.
// Returns the new file paths.
func (e *Extractorr) MoveFiles(fromPath string, toPath string) ([]string, error) {
	files := e.GetFileList(fromPath)

	var keepErr error

	for i, file := range files {
		newFile := filepath.Join(toPath, filepath.Base(file))
		if err := os.Rename(file, newFile); err != nil {
			keepErr = err
			e.log("Error: Renaming Temp File: %v to %v: %v", file, newFile, err)
			// keep trying.
			continue
		}

		files[i] = newFile
		e.debug("Renamed Temp File: %v -> %v", file, files[i])
	}

	e.DeleteFiles(fromPath)

	// Since this is the last step, we tried to rename all the files, bubble the
	// os.Rename error up, so it gets flagged as failed. It may have worked, but
	// it should get attention.
	return files, keepErr
}

// DeleteFiles obliterates things and logs. Use with caution.
func (e *Extractorr) DeleteFiles(files ...string) {
	for _, file := range files {
		if err := os.RemoveAll(file); err != nil {
			e.log("Error: Deleting %v: %v", file, err)
			continue
		}

		e.log("Deleted (recursively): %s", file)
	}
}

// WriteNewFile writes a file from an io reader, making sure all parent directories exist.
func WriteNewFile(fpath string, fdata io.Reader, fmode os.FileMode) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
		return 0, err
	}

	fout, err := os.Create(fpath)
	if err != nil {
		return 0, err
	}
	defer fout.Close()

	if runtime.GOOS != "windows" {
		if err = fout.Chmod(fmode); err != nil {
			return 0, err
		}
	}

	return io.Copy(fout, fdata)
}
