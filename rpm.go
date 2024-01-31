package xtractr

import (
	"compress/bzip2"
	"compress/gzip"
	"fmt"
	"io"
	"os"

	"github.com/cavaliergopher/rpm"
	"github.com/klauspost/compress/zstd"
	"github.com/therootcompany/xz"
	"github.com/ulikunitz/xz/lzma"
)

var (
	ErrUnsupportedRPMCompression = fmt.Errorf("unsupported rpm compression")
	ErrUnsupportedRPMArchiveFmt  = fmt.Errorf("unsupported rpm archive format")
)

func ExtractRPM(xFile *XFile) (int64, []string, error) {
	rpmFile, err := os.Open(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("os.Open: %w", err)
	}
	defer rpmFile.Close()

	// Read the package headers
	pkg, err := rpm.Read(rpmFile)
	if err != nil {
		return 0, nil, fmt.Errorf("rpm.Read: %w", err)
	}

	// Check the RPM compression algorithm.
	switch compression := pkg.PayloadCompression(); compression {
	case "xz":
		zipReader, err := xz.NewReader(rpmFile, 0)
		if err != nil {
			return 0, nil, fmt.Errorf("xz.NewReader: %w", err)
		}

		return xFile.unrpm(zipReader, pkg.PayloadFormat())
	case "gz", "gzip":
		zipReader, err := gzip.NewReader(rpmFile)
		if err != nil {
			return 0, nil, fmt.Errorf("gzip.NewReader: %w", err)
		}
		defer zipReader.Close()

		return xFile.unrpm(zipReader, pkg.PayloadFormat())
	case "bz2", "bzip2":
		return xFile.unrpm(bzip2.NewReader(rpmFile), pkg.PayloadFormat())
	case "zstd", "zstandard", "zst", "Zstandard":
		zipReader, err := zstd.NewReader(rpmFile)
		if err != nil {
			return 0, nil, fmt.Errorf("zstd.NewReader: %w", err)
		}
		defer zipReader.Close()

		return xFile.unrpm(zipReader, pkg.PayloadFormat())
	case "lzma2":
		zipReader, err := lzma.NewReader2(rpmFile)
		if err != nil {
			return 0, nil, fmt.Errorf("lzma.NewReader2: %w", err)
		}

		return xFile.unrpm(zipReader, pkg.PayloadFormat())
	case "lzma", "lzip":
		zipReader, err := lzma.NewReader(rpmFile)
		if err != nil {
			return 0, nil, fmt.Errorf("lzma.NewReader: %w", err)
		}

		return xFile.unrpm(zipReader, pkg.PayloadFormat())
	default:
		return 0, nil, fmt.Errorf("%w: %s", ErrUnsupportedRPMCompression, compression)
	}
}

func (x *XFile) unrpm(reader io.Reader, format string) (int64, []string, error) {
	// Check the archive format of the payload
	switch format {
	case "cpio":
		return x.uncpio(reader)
	case "tar":
		return x.untar(reader)
	case "ar":
		return x.unAr(reader)
	default:
		return 0, nil, fmt.Errorf("%w: %s", ErrUnsupportedRPMArchiveFmt, format)
	}
}
