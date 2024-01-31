package xtractr

/* Code to find, write, move and delete files. */

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// https://github.com/golift/xtractr/issues/44
//
//nolint:gochecknoglobals
var extension2function = map[string]func(*XFile) (int64, []string, []string, error){
	".tar.bz":  fixOutput(ExtractTarBzip),
	".tar.bz2": fixOutput(ExtractTarBzip),
	".tar.gz":  fixOutput(ExtractTarGzip),
	".tar.xz":  fixOutput(ExtractTarXZ),
	".tar.z":   fixOutput(ExtractTarZ),
	// The ones with double extensions that match a single (below) need to come first.
	".7z":     Extract7z,
	".7z.001": Extract7z,
	".z":      fixOutput(ExtractLZW), // everything is lowercase...
	".br":     fixOutput(ExtractBrotli),
	".brotli": fixOutput(ExtractBrotli),
	".bz":     fixOutput(ExtractBzip),
	".bz2":    fixOutput(ExtractBzip),
	".gz":     fixOutput(ExtractGzip),
	".gzip":   fixOutput(ExtractGzip),
	".iso":    fixOutput(ExtractISO),
	".lz4":    fixOutput(ExtractLZ4),
	".r00":    ExtractRAR,
	".rar":    ExtractRAR,
	".s2":     fixOutput(ExtractS2),
	".snappy": fixOutput(ExtractSnappy),
	".sz":     fixOutput(ExtractSnappy),
	".tar":    fixOutput(ExtractTar),
	".tbz":    fixOutput(ExtractTarBzip),
	".tbz2":   fixOutput(ExtractTarBzip),
	".tgz":    fixOutput(ExtractTarGzip),
	".txz":    fixOutput(ExtractTarXZ),
	".tz":     fixOutput(ExtractTarZ),
	".xz":     fixOutput(ExtractXZ),
	".zip":    fixOutput(ExtractZIP),
	".zlib":   fixOutput(ExtractZlib),
	".zst":    fixOutput(ExtractZstandard),
	".zstd":   fixOutput(ExtractZstandard),
	".zz":     fixOutput(ExtractZlib),
}

// SupportedExtensions returns a slice of file extensions this library recognizes.
func SupportedExtensions() []string {
	exts := make([]string, len(extension2function))
	count := 0

	for ext := range extension2function {
		exts[count] = ext
		count++
	}

	return exts
}

// XFile defines the data needed to extract an archive.
type XFile struct {
	// Path to archive being extracted.
	FilePath string
	// Folder to extract archive into.
	OutputDir string
	// Write files with this mode.
	FileMode os.FileMode
	// Write folders with this mode.
	DirMode os.FileMode
	// (RAR/7z) Archive password. Blank for none. Gets prepended to Passwords, below.
	Password string
	// (RAR/7z) Archive passwords (to try multiple).
	Passwords []string
}

// Filter is the input to find compressed files.
type Filter struct {
	// This is the path to search in for archives.
	Path string
	// Any files with this suffix are ignored. ie. ".7z" or ."iso"
	ExcludeSuffix Exclude
	// Count of folder depth allowed when finding archives. 1 = root
	MaxDepth int
	// Only find archives this many child-folders deep.
	MinDepth int
}

// Exclude represents an exclusion list.
type Exclude []string

// GetFileList returns all the files in a path.
// This is non-resursive and only returns files _in_ the base path provided.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) GetFileList(path string) ([]string, error) {
	if stat, err := os.Stat(path); err == nil && !stat.IsDir() {
		return []string{path}, nil
	}

	fileList, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("reading path %s: %w", path, err)
	}

	files := make([]string, len(fileList))
	for idx, file := range fileList {
		files[idx] = filepath.Join(path, file.Name())
	}

	return files, nil
}

// Difference returns all the strings that are in slice2 but not in slice1.
// Used to find new files in a file list from a path. ie. those we extracted.
// This is a helper method and only exposed for convenience. You do not have to call this.
func Difference(slice1 []string, slice2 []string) []string {
	diff := []string{}

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

// Has returns true if the test has an excluded suffix.
func (e Exclude) Has(test string) bool {
	for _, exclude := range e {
		if strings.HasSuffix(test, strings.ToLower(exclude)) {
			return true
		}
	}

	return false
}

// FindCompressedFiles returns all the compressed archive files in a path. This attempts to grab
// only the first file in a multi-part rar or 7zip archive. Sometimes there are multiple archives,
// so if the rar archive does not have "part" followed by a number in the name, then it will be
// considered an independent archive. Some packagers seem to use different naming schemes,
// so this may need to be updated as time progresses. Use the input to filter to adjust the output.
func FindCompressedFiles(filter Filter) map[string][]string {
	return findCompressedFiles(filter.Path, &filter, 0)
}

func findCompressedFiles(path string, filter *Filter, depth int) map[string][]string {
	if filter.MaxDepth > 0 && filter.MaxDepth < depth {
		return nil
	}

	dir, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer dir.Close()

	if info, err := dir.Stat(); err != nil {
		return nil // unreadable folder?
	} else if !info.IsDir() && isArchiveFile(path) {
		return map[string][]string{path: {path}} // passed in an archive file; send it back out.
	}

	fileList, err := dir.Readdir(-1)
	if err != nil {
		return nil
	}

	// Check (save) if the current path has any rar files.
	// So we can ignore r00 if it does.
	r, _ := filepath.Glob(filepath.Join(path, "*.rar"))

	return getCompressedFiles(len(r) > 0, path, filter, fileList, depth)
}

func isArchiveFile(path string) bool {
	path = strings.ToLower(path)

	for extension := range extension2function {
		if strings.HasSuffix(path, extension) {
			return true
		}
	}

	return false
}

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles( //nolint:cyclop
	hasrar bool,
	path string,
	filter *Filter,
	fileList []os.FileInfo,
	depth int,
) map[string][]string {
	files := map[string][]string{}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case !file.IsDir() &&
			(filter.ExcludeSuffix.Has(lowerName) || depth < filter.MinDepth):
			continue // file suffix is excluded or we are not deep enough.
		case lowerName == "" || lowerName[0] == '.':
			continue // ignore empty names and dot files/folders.
		case file.IsDir(): // Recurse.
			for k, v := range findCompressedFiles(filepath.Join(path, file.Name()), filter, depth+1) {
				files[k] = v
			}
		case strings.HasSuffix(lowerName, ".rar"):
			hasParts := regexp.MustCompile(`.*\.part[0-9]+\.rar$`)
			partOne := regexp.MustCompile(`.*\.part0*1\.rar$`)
			// Some archives are named poorly. Only return part01 or part001, not all.
			if !hasParts.MatchString(lowerName) || partOne.MatchString(lowerName) {
				files[path] = append(files[path], filepath.Join(path, file.Name()))
			}
		case !hasrar && strings.HasSuffix(lowerName, ".r00"):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			files[path] = append(files[path], filepath.Join(path, file.Name()))
		case !strings.HasSuffix(lowerName, ".r00") && isArchiveFile(lowerName):
			files[path] = append(files[path], filepath.Join(path, file.Name()))
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
	sName := strings.ToLower(xFile.FilePath)

	for extension, extract := range extension2function {
		if strings.HasSuffix(sName, extension) {
			return extract(xFile)
		}
	}

	return 0, nil, nil, fmt.Errorf("%w: %s", ErrUnknownArchiveType, xFile.FilePath)
}

// Functions with multi-part archive files return four values. Other functions return only 3.
// This fixOutput function makes both interfaces compatible.
func fixOutput(small func(*XFile) (int64, []string, error)) func(*XFile) (int64, []string, []string, error) {
	return func(xFile *XFile) (int64, []string, []string, error) {
		size, files, err := small(xFile)
		return size, files, []string{xFile.FilePath}, err
	}
}

// MoveFiles relocates files then removes the folder they were in.
// Returns the new file paths.
// This is a helper method and only exposed for convenience. You do not have to call this.
func (x *Xtractr) MoveFiles(fromPath string, toPath string, overwrite bool) ([]string, error) { //nolint:cyclop
	var (
		newFiles = []string{}
		keepErr  error
	)

	files, err := x.GetFileList(fromPath)
	if err != nil {
		return nil, err
	}

	// If the "to path" is an existing archive file, remove the suffix to make a directory.
	if _, err := os.Stat(toPath); err == nil && isArchiveFile(toPath) {
		toPath = strings.TrimSuffix(toPath, filepath.Ext(toPath))
	}

	if err := os.MkdirAll(toPath, x.config.DirMode); err != nil {
		return nil, fmt.Errorf("os.MkdirAll: %w", err)
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

	fout, err := os.OpenFile(fpath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fMode)
	if err != nil {
		return 0, fmt.Errorf("os.OpenFile: %w", err)
	}
	defer fout.Close()

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

// AllExcept can be used as an input to ExcludeSuffix in a Filter.
// Returns a list of supported extensions minus the ones provided.
// Extensions for like-types such as .rar and .r00 need to both be provided.
// Same for .tar.gz and .tgz variants.
func AllExcept(onlyThese []string) []string {
	output := SupportedExtensions()

	for _, str := range onlyThese {
		idx := 0

		for _, ext := range output {
			if !strings.EqualFold(ext, str) {
				output[idx] = ext
				idx++
			}
		}

		output = output[:idx]
	}

	return output
}
