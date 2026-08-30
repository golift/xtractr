package xtractr

/* Code to find compressed archive files. */

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ArchiveList is the value returned when searching for compressed files.
// The map is directory to list of archives in that directory.
type ArchiveList map[string][]string

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
	// MaxArchives stops the walk after this many archives are found. 0 is unlimited.
	MaxArchives int
	// AllowSymlinks includes symlink-named archive files in the listing.
	// Default false. Symlink directories found during the walk are never
	// followed; a symlink passed as Path is (operator-chosen search root).
	// The extras/recursion pass always ignores this and never lists archive-member links.
	AllowSymlinks bool
	// Accept, if set, is called when Find is about to add an archive.
	// Return false to skip it; skipped paths do not count toward MaxArchives.
	Accept func(ArchiveCandidate) bool
}

// ArchiveCandidate is passed to Filter.Accept when Find is about to list a path.
type ArchiveCandidate struct {
	// Path is the archive file being considered.
	Path string
	// Info is the Lstat of Path. May be nil if the path could not be stated.
	Info os.FileInfo
}

// Exclude represents an exclusion list.
type Exclude []string

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
func FindCompressedFiles(filter Filter) ArchiveList { //nolint:gocritic // public API; Filter is a value.
	return findCompressedFiles(filter.Path, &filter, 0)
}

func findCompressedFiles(path string, filter *Filter, depth int) ArchiveList {
	if filter.MaxDepth > 0 && filter.MaxDepth < depth {
		return nil
	}

	dir, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer dir.Close()

	info, err := dir.Stat()
	if err != nil {
		return nil // unreadable folder?
	}

	if !info.IsDir() && IsArchiveFile(path) {
		if skipSymlinkArchivePath(filter, path) {
			return nil
		}

		if linkInfo, _ := os.Lstat(path); !acceptArchive(filter, path, linkInfo) {
			return nil
		}

		return ArchiveList{path: {path}} // passed in an archive file; send it back out.
	}

	fileList := getFilteredFileList(path, dir)
	if len(fileList) == 0 {
		return nil
	}

	return getCompressedFiles(path, filter, fileList, depth)
}

// getFilteredFileList reads the directory and returns a list of readable files that are not dot files.
func getFilteredFileList(path string, dir *os.File) []os.FileInfo {
	names, _ := dir.Readdirnames(-1)
	fileList := make([]os.FileInfo, 0, len(names))

	for _, name := range names {
		if name == "" || name[0] == '.' {
			continue // skip dot files (including AppleDouble ._* entries)
		}

		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil {
			continue // skip entries we can't stat
		}

		fileList = append(fileList, info)
	}

	return fileList
}

// CheckR00ForRarFile scans the file list to determine if a .rar file with the same name as .r00 exists.
// Returns true if the r00 files has an accompanying rar file in the fileList.
func CheckR00ForRarFile(fileList []os.FileInfo, r00file string) bool {
	findFile := strings.TrimSuffix(strings.TrimSuffix(r00file, ".R00"), ".r00") + ".rar"

	for _, file := range fileList {
		if strings.EqualFold(file.Name(), findFile) {
			return true
		}
	}

	return false
}

// RAR multipart name matchers. Compiled once; reused for every directory in a Find walk.
// `$` already anchors the end, so a leading `.*` is unnecessary.
var (
	rarHasParts = regexp.MustCompile(`\.part\d+\.rar$`)
	rarPartOne  = regexp.MustCompile(`\.part0*1\.rar$`)
)

// getCompressedFiles checks file suffixes to find archives to decompress.
// This pays special attention to the widely accepted variance of rar formats.
func getCompressedFiles(path string, filter *Filter, fileList []os.FileInfo, depth int) ArchiveList { //nolint:cyclop
	files := ArchiveList{}

	for _, file := range fileList {
		switch lowerName := strings.ToLower(file.Name()); {
		case archivesAtCap(files, filter):
			return files
		case file.Mode()&os.ModeSymlink != 0 &&
			(!filter.AllowSymlinks || symlinkTargetsDir(path, file.Name())):
			continue // skip symlink archives unless allowed; never walk symlink dirs.
		case !file.IsDir() &&
			(filter.ExcludeSuffix.Has(lowerName) || depth < filter.MinDepth):
			continue // file suffix is excluded or we are not deep enough.
		case lowerName == "" || lowerName[0] == '.':
			continue // ignore empty names and dot files/folders.
		case file.IsDir(): // Recurse.
			maps.Copy(files, findCompressedFiles(filepath.Join(path, file.Name()), filter, depth+1))
		case strings.HasSuffix(lowerName, ".rar"):
			// Some archives are named poorly. Only return part01 or part001, not all.
			if !rarHasParts.MatchString(lowerName) || rarPartOne.MatchString(lowerName) {
				addArchive(files, path, file, filter)
			}
		case strings.HasSuffix(lowerName, ".r00") && !CheckR00ForRarFile(fileList, lowerName):
			// Accept .r00 as the first archive file if no .rar files are present in the path.
			addArchive(files, path, file, filter)
		case !strings.HasSuffix(lowerName, ".r00") && IsArchiveFile(lowerName):
			addArchive(files, path, file, filter)
		}
	}

	return files
}

func addArchive(files ArchiveList, dir string, file os.FileInfo, filter *Filter) {
	path := filepath.Join(dir, file.Name())
	if acceptArchive(filter, path, file) {
		files[dir] = append(files[dir], path)
	}
}

func acceptArchive(filter *Filter, path string, info os.FileInfo) bool {
	return filter == nil || filter.Accept == nil || filter.Accept(ArchiveCandidate{Path: path, Info: info})
}

func archivesAtCap(files ArchiveList, filter *Filter) bool {
	return filter != nil && filter.MaxArchives > 0 && files.Count() >= filter.MaxArchives
}

// AllExcept can be used as an input to ExcludeSuffix in a Filter.
// Returns a list of supported extensions minus the ones provided.
// Extensions for like-types such as .rar and .r00 need to both be provided.
// Same for .tar.gz and .tgz variants. Passing .cue also keeps .cue.txt.
func AllExcept(onlyThese ...string) Exclude {
	keep := make([]string, 0, len(onlyThese)+1)
	keep = append(keep, onlyThese...)

	for _, str := range onlyThese {
		if strings.EqualFold(str, ".cue") {
			keep = append(keep, ".cue.txt")
			break
		}
	}

	// Start by excluding everything.
	output := SupportedExtensions()

	// Loop through the extensions we want to keep.
	for _, str := range keep {
		idx := 0
		// Remove each one from the output list.
		for _, ext := range output {
			if !strings.EqualFold(ext, str) {
				output[idx] = ext
				idx++
			}
		}
		// Truncate the output to the size of items kept.
		output = output[:idx]
	}

	return output
}

// Count returns the number of unique archives in the archive list.
func (a ArchiveList) Count() int {
	var count int

	for _, files := range a {
		count += len(files)
	}

	return count
}

// Random returns a random file listing from the archive list.
// If the list only contains one directory, then that is the one returned.
// If the archive list is empty or nil, returns nil.
func (a ArchiveList) Random() []string {
	for _, files := range a {
		return files
	}

	return nil
}

// List returns all of the archives as a string slice.
func (a ArchiveList) List() []string {
	list := make([]string, 0, len(a))

	for _, files := range a {
		list = append(list, files...)
	}

	return list
}

func skipSymlinkArchivePath(filter *Filter, path string) bool {
	if filter != nil && filter.AllowSymlinks {
		return false
	}

	linkInfo, linkErr := os.Lstat(path)

	return linkErr != nil || linkInfo.Mode()&os.ModeSymlink != 0
}

func symlinkTargetsDir(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err != nil || info.IsDir()
}
