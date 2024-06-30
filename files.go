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

type archive struct {
	// Extension is passed to strings.HasSuffix.
	Extension string
	// Extract function for this extension.
	Extract Interface
}

// Interface is a common interface for extracting compressed or non-compressed files or archives.
type Interface func(*XFile) (int64, []string, []string, error)

// https://github.com/golift/xtractr/issues/44
//
//nolint:gochecknoglobals
var extension2function = []archive{
	{Extension: ".tar.bz2", Extract: ChngInt(ExtractTarBzip)},
	{Extension: ".tar.gz", Extract: ChngInt(ExtractTarGzip)},
	{Extension: ".tar.xz", Extract: ChngInt(ExtractTarXZ)},
	{Extension: ".tar.z", Extract: ChngInt(ExtractTarZ)},
	// The ones with double extensions that match a single (below) need to come first.
	{Extension: ".7z", Extract: Extract7z},
	{Extension: ".7z.001", Extract: Extract7z},
	{Extension: ".z", Extract: ChngInt(ExtractLZW)}, // everything is lowercase...
	{Extension: ".br", Extract: ChngInt(ExtractBrotli)},
	{Extension: ".brotli", Extract: ChngInt(ExtractBrotli)},
	{Extension: ".bz2", Extract: ChngInt(ExtractBzip)},
	{Extension: ".gz", Extract: ChngInt(ExtractGzip)},
	{Extension: ".gzip", Extract: ChngInt(ExtractGzip)},
	{Extension: ".iso", Extract: ChngInt(ExtractISO)},
	{Extension: ".lz4", Extract: ChngInt(ExtractLZ4)},
	{Extension: ".r00", Extract: ExtractRAR},
	{Extension: ".rar", Extract: ExtractRAR},
	{Extension: ".s2", Extract: ChngInt(ExtractS2)},
	{Extension: ".snappy", Extract: ChngInt(ExtractSnappy)},
	{Extension: ".sz", Extract: ChngInt(ExtractSnappy)},
	{Extension: ".tar", Extract: ChngInt(ExtractTar)},
	{Extension: ".tbz", Extract: ChngInt(ExtractTarBzip)},
	{Extension: ".tbz2", Extract: ChngInt(ExtractTarBzip)},
	{Extension: ".tgz", Extract: ChngInt(ExtractTarGzip)},
	{Extension: ".txz", Extract: ChngInt(ExtractTarXZ)},
	{Extension: ".tz", Extract: ChngInt(ExtractTarZ)},
	{Extension: ".xz", Extract: ChngInt(ExtractXZ)},
	{Extension: ".zip", Extract: ChngInt(ExtractZIP)},
	{Extension: ".zlib", Extract: ChngInt(ExtractZlib)},
	{Extension: ".zst", Extract: ChngInt(ExtractZstandard)},
	{Extension: ".zstd", Extract: ChngInt(ExtractZstandard)},
	{Extension: ".zz", Extract: ChngInt(ExtractZlib)},
}

// ChngInt converts the smaller return interface into an ExtractInterface.
// Functions with multi-part archive files return four values. Other functions return only 3.
// This ChngInt function makes both interfaces compatible.
func ChngInt(smallFn func(*XFile) (int64, []string, error)) Interface {
	return func(xFile *XFile) (int64, []string, []string, error) {
		size, files, err := smallFn(xFile)
		return size, files, []string{xFile.FilePath}, err
	}
}

// SupportedExtensions returns a slice of file extensions this library recognizes.
func SupportedExtensions() []string {
	exts := make([]string, len(extension2function))

	for idx, ext := range extension2function {
		exts[idx] = ext.Extension
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
	// Any files with this suffix are ignored. ie. ".7z" or ".iso"
	// Use the AllExcept func to create an inclusion list instead.
	ExcludeSuffix Exclude
	// Count of folder depth allowed when finding archives. 1 = root
	MaxDepth int
	// Only find archives this many child-folders deep. 0 and 1 are equal.
	MinDepth int
}

// Exclude represents an exclusion list.
type Exclude []string

// GetFileList returns all the files in a path.
// This is non-recursive and only returns files _in_ the base path provided.
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
// so this may need to be updated as time progresses. Use the input to Filter to adjust the output.
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

	return getCompressedFiles(path, filter, fileList, depth)
}

func isArchiveFile(path string) bool {
	path = strings.ToLower(path)

	for _, ext := range extension2function {
		if strings.HasSuffix(path, ext.Extension) {
			return true
		}
	}

	return false
}

// checkR00ForRarFile scans the file list to determine if a .rar file with the same name as .r00 exists.
func checkR00ForRarFile(fileList []os.FileInfo, r00file string) bool {
	findFile := strings.TrimSuffix(r00file, ".r00") + ".rar"

	for _, file := range fileList {
		if strings.EqualFold(file.Name(), findFile) {
			return true
		}
	}

	return false
}

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles( //nolint:cyclop
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
		case strings.HasSuffix(lowerName, ".r00") && !checkR00ForRarFile(fileList, lowerName):
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

	for _, ext := range extension2function {
		if strings.HasSuffix(sName, ext.Extension) {
			return ext.Extract(xFile)
		}
	}

	return 0, nil, nil, fmt.Errorf("%w: %s", ErrUnknownArchiveType, xFile.FilePath)
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
func AllExcept(onlyThese []string) Exclude {
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
