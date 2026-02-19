package xtractr

import (
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"

	"github.com/cavaliergopher/rpm"
	"github.com/klauspost/compress/zstd"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

// ExtractRPM extract a file as a RedHat Package Manager file.
func ExtractRPM(xFile *XFile) (size uint64, filesList []string, err error) {
	rpmFile, stat, err := openStatFile(xFile.FilePath)
	if err != nil {
		return 0, nil, err
	}
	defer rpmFile.Close()

	defer xFile.newProgress(0, uint64(stat.Size()), 0).done()

	files, err := xFile.extractRPM(xFile.prog.reader(rpmFile))

	return xFile.prog.Wrote, files, err
}

func (x *XFile) extractRPM(rpmFile io.Reader) (filesList []string, err error) { //nolint:cyclop
	// Read the package headers
	pkg, err := rpm.Read(rpmFile)
	if err != nil {
		return nil, fmt.Errorf("rpm.Read: %w", err)
	}

	// Check the RPM compression algorithm.
	switch compression := pkg.PayloadCompression(); compression {
	case "xz":
		zipReader, err := xz.NewReader(rpmFile, 0)
		if err != nil {
			return nil, fmt.Errorf("xz.NewReader: %w", err)
		}

		return x.unrpm(zipReader, pkg.PayloadFormat())
	case "gz", "gzip":
		zipReader, err := gzip.NewReader(rpmFile)
		if err != nil {
			return nil, fmt.Errorf("gzip.NewReader: %w", err)
		}
		defer zipReader.Close()

		return x.unrpm(zipReader, pkg.PayloadFormat())
	case "bz2", "bzip2":
		return x.unrpm(bzip2.NewReader(rpmFile), pkg.PayloadFormat())
	case "zstd", "zstandard", "zst", "Zstandard":
		zipReader, err := zstd.NewReader(rpmFile)
		if err != nil {
			return nil, fmt.Errorf("zstd.NewReader: %w", err)
		}
		defer zipReader.Close()

		return x.unrpm(zipReader, pkg.PayloadFormat())
	case "lzma2":
		zipReader, err := lzma.NewReader2(rpmFile)
		if err != nil {
			return nil, fmt.Errorf("lzma.NewReader2: %w", err)
		}

		return x.unrpm(zipReader, pkg.PayloadFormat())
	case "lzma", "lzip":
		zipReader, err := lzma.NewReader(rpmFile)
		if err != nil {
			return nil, fmt.Errorf("lzma.NewReader: %w", err)
		}

		return x.unrpm(zipReader, pkg.PayloadFormat())
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRPMCompression, compression)
	}
}

func (x *XFile) unrpm(reader io.Reader, format string) (filesList []string, err error) {
	// Check the archive format of the payload
	switch format {
	case "cpio":
		return x.uncpio(reader)
	case "tar":
		return x.untar(reader)
	case "ar":
		return x.unAr(reader)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedRPMArchiveFmt, format)
	}
}
