package xtractr

import (
	"archive/tar"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	lzw "github.com/sshaman1101/dcompress"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

// ExtractTar extracts a raw (non-compressed) tar archive.
func ExtractTar(xFile *XFile) (size uint64, filesList []string, err error) {
	tarFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer tarFile.Close()

	defer xFile.newProgress(uint64(stat.Size()), uint64(stat.Size()), 0).done()

	files, err := xFile.untar(xFile.prog.reader(tarFile))

	return xFile.prog.Wrote, files, err
}

// ExtractTarBzip extracts a bzip2-compressed tar archive.
func ExtractTarBzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	files, err := xFile.untar(bzip2.NewReader(xFile.prog.reader(compressedFile)))

	return xFile.prog.Wrote, files, err
}

// ExtractTarXZ extracts an XZ-compressed tar archive (txz).
func ExtractTarXZ(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := xz.NewReader(xFile.prog.reader(compressedFile), 0)
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarZ extracts an LZW-compressed tar archive (tz).
func ExtractTarZ(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := lzw.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("lzw.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarGzip extracts a gzip-compressed tar archive (tgz).
func ExtractTarGzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := gzip.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
	}
	defer zipStream.Close()

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// ExtractTarLzip extracts an LZIP-compressed tar archive (tlz).
func ExtractTarLzip(xFile *XFile) (size uint64, filesList []string, err error) {
	compressedFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer compressedFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	zipStream, err := lzma.NewReader(xFile.prog.reader(compressedFile))
	if err != nil {
		return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
	}

	files, err := xFile.untar(zipStream)

	return xFile.prog.Wrote, files, err
}

// errSkipEntry is returned for non-fatal archive members that should be ignored.
var errSkipEntry = errors.New("skip archive entry")

func (x *XFile) untar(reader io.Reader) ([]string, error) {
	tarReader := tar.NewReader(reader)
	files := []string{}

	for {
		header, err := tarReader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return files, fmt.Errorf("%s: tarReader.Next: %w", x.FilePath, err)
		}

		fSize, err := x.untarFile(header, tarReader)
		if errors.Is(err, errSkipEntry) {
			continue
		}

		if err != nil {
			return files, err
		}

		files = append(files, header.Name)
		x.Debugf("Wrote archived file: %s (%d bytes), total: %d files and %d bytes",
			header.Name, fSize, x.prog.Files, x.prog.Wrote)
	}

	files, err := x.cleanup(files)

	return files, err
}

func (x *XFile) untarFile(header *tar.Header, tarReader *tar.Reader) (uint64, error) {
	file := &file{
		Path:     x.clean(header.Name),
		Data:     tarReader,
		FileMode: header.FileInfo().Mode(),
		DirMode:  x.DirMode,
		Mtime:    header.ChangeTime,
		Atime:    header.AccessTime,
	}

	if header.Format != tar.FormatGNU && header.Format != tar.FormatPAX {
		file.Mtime = header.ModTime
		file.Atime = time.Now()
	}

	if !x.pathWithinOutput(file.Path) {
		// The file being written is trying to write outside of our base path. Malicious archive?
		return 0, fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, file.Path, header.Name)
	}

	switch header.Typeflag {
	case tar.TypeDir:
		x.Debugf("Writing archived directory: %s", file.Path)

		err := x.mkDir(file.Path, header.FileInfo().Mode(), header.ModTime)
		if err != nil {
			return 0, fmt.Errorf("making tar file dir: %w", err)
		}

		return 0, nil
	case tar.TypeSymlink, tar.TypeLink:
		// Symlinks (and hard links) have no file payload; writing them as regular
		// files produces empty stubs — see https://github.com/golift/xtractr/issues/153
		return x.untarLink(header, file.Path)
	}

	x.Debugf("Writing archived file: %s (bytes: %d)", file.Path, header.FileInfo().Size())

	return x.write(file)
}

// resolveLinkTarget returns the cleaned filesystem path a link would resolve to.
func resolveLinkTarget(linkPath, linkName string) string {
	if filepath.IsAbs(linkName) {
		return filepath.Clean(linkName)
	}

	return filepath.Clean(filepath.Join(filepath.Dir(linkPath), linkName))
}

// ensureLinkWithinOutput rejects symlink targets that escape OutputDir.
func (x *XFile) ensureLinkWithinOutput(linkPath, linkName string) error {
	resolved := resolveLinkTarget(linkPath, linkName)
	if !x.pathWithinOutput(resolved) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, resolved, linkName)
	}

	return nil
}

// untarLink creates a symlink or hard link from a tar header.
func (x *XFile) untarLink(header *tar.Header, path string) (uint64, error) {
	err := x.mkDir(filepath.Dir(path), x.DirMode, header.ModTime)
	if err != nil {
		return 0, fmt.Errorf("making tar link parent dir: %w", err)
	}

	err = os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("%s: removing existing path for link: %w: %s", x.FilePath, err, path)
	}

	switch header.Typeflag {
	case tar.TypeSymlink:
		return 0, x.createSymlink(path, header.Linkname)
	case tar.TypeLink:
		return 0, x.createHardLink(path, header.Linkname)
	}

	return 0, nil
}

func (x *XFile) createSymlink(path, linkName string) error {
	if linkName == "" {
		x.Printf("Warning: skipping symlink with empty target: %s", path)

		return errSkipEntry
	}

	err := x.ensureLinkWithinOutput(path, linkName)
	if err != nil {
		return err
	}

	x.Debugf("Writing archived symlink: %s -> %s", path, linkName)

	err = os.Symlink(linkName, path)
	if err != nil {
		return fmt.Errorf("%s: creating symlink: %w: %s -> %s", x.FilePath, err, path, linkName)
	}

	return nil
}

func (x *XFile) createHardLink(path, linkName string) error {
	if linkName == "" {
		x.Printf("Warning: skipping hard link with empty target: %s", path)

		return errSkipEntry
	}

	// Hard-link names are archive member paths, not arbitrary filesystem paths.
	if filepath.IsAbs(linkName) {
		return fmt.Errorf("%s: %w: %s", x.FilePath, ErrInvalidPath, linkName)
	}

	target := x.clean(linkName)
	if !x.pathWithinOutput(target) {
		return fmt.Errorf("%s: %w: %s (from: %s)", x.FilePath, ErrInvalidPath, target, linkName)
	}

	x.Debugf("Writing archived hard link: %s => %s", path, target)

	err := os.Link(target, path)
	if err == nil {
		return nil
	}

	linkErr := err

	rel, relErr := filepath.Rel(filepath.Dir(path), target)
	if relErr != nil {
		return fmt.Errorf("%s: creating hard link: %w: %s => %s", x.FilePath, linkErr, path, target)
	}

	// Fall back to a relative symlink when hard links are unavailable
	// (e.g. target not extracted yet, or the filesystem does not support them).
	x.Debugf("Hard link failed (%v); falling back to symlink: %s -> %s", linkErr, path, rel)

	return x.createSymlink(path, rel)
}
