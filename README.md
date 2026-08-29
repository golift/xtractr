# `xtractr`

Go Library for Queuing and Extracting ZIP, RAR, GZ, BZ2, TAR,
TGZ, TBZ2, 7Z, ISO ([and other](https://github.com/golift/xtractr/issues/44)) compressed archive files.
Can also be used ad-hoc for direct decompression and extraction. See [GoDoc](https://pkg.go.dev/golift.io/xtractr).

- [GoDoc](https://pkg.go.dev/golift.io/xtractr)
- Works on Linux, Windows, FreeBSD and macOS **without Cgo**.
- Supports 32 and 64 bit architectures.
- Decrypts RAR and 7-Zip archives with passwords.
- Extracts ISO images (ISO9660 and UDF volumes).
- Splits FLAC+CUE sheets into individual tracks.
- Detects non-UTF8 zip filenames automatically.

## Interface

This library provides a queue, and a common interface to extract files.
It does not do the heavy lifting, and relies on these libraries to extract files:

- [**RAR**: nwaples/rardecode](https://github.com/nwaples/rardecode)
- [**7-Zip**: bodgit/sevenzip](https://github.com/bodgit/sevenzip)
- [**ISO**: kdomanski/iso9660](https://github.com/kdomanski/iso9660)
- [**UDF**: golift/udf](https://github.com/golift/udf)
- [**FLAC**: mewkiz/flac](https://github.com/mewkiz/flac)
- [**Brotli**: andybalholm/brotli](https://github.com/andybalholm/brotli)
- [**LZ4**: pierrec/lz4](https://github.com/pierrec/lz4)
- [**XZ**: therootcompany/xz](https://github.com/therootcompany/xz)
- [**Zstandard**: klauspost/compress](https://github.com/klauspost/compress)
- [**S2**: klauspost/compress](https://github.com/klauspost/compress)
- [**Snappy**: klauspost/compress](https://github.com/klauspost/compress)
- [**Zlib**: klauspost/compress](https://github.com/klauspost/compress)
- [**LZW**: sshaman1101/dcompress](https://github.com/sshaman1101/dcompress)

`Zip`, `Gzip`, `Tar` and `Bzip` are all handled by the standard Go library.

## Examples

### Example 1 - Queue

```golang
package main

import (
	"log"
	"os"
	"strings"

	"golift.io/xtractr"
)

// Logger satisfies the xtractr.Logger interface.
type Logger struct {
	xtractr *log.Logger
	debug   *log.Logger
	info    *log.Logger
}

// Printf satisfies the xtractr.Logger interface.
func (l *Logger) Printf(msg string, v ...interface{}) {
	l.xtractr.Printf(msg, v...)
}

// Debug satisfies the xtractr.Logger interface.
func (l *Logger) Debugf(msg string, v ...interface{}) {
	l.debug.Printf(msg, v...)
}

// Infof printf an info line.
func (l *Logger) Infof(msg string, v ...interface{}) {
	l.info.Printf(msg, v...)
}

func main() {
	log := &Logger{
		xtractr: log.New(os.Stdout, "[XTRACTR] ", 0),
		debug:   log.New(os.Stdout, "[DEBUG] ", 0),
		info:    log.New(os.Stdout, "[INFO] ", 0),
	}
	q := xtractr.NewQueue(&xtractr.Config{
		Suffix:   "_xtractd",
		Logger:   log,
		Parallel: 1,
		FileMode: 0644, // ignored for tar files.
		DirMode:  0755,
	})
	defer q.Stop() // Stop() waits until all extractions finish.

	response := make(chan *xtractr.Response)
	// This sends an item into the extraction queue (buffered channel).
	q.Extract(&xtractr.Xtract{
		Name:      "my archive",    // unused by this library.
		Path:      "/tmp/archives", // can also be a direct file.
		CBChannel: response,        // queue responses are sent here.
	})

	// Queue always sends two responses. 1 on start and again when finished (error or not)
	resp := <-response
	log.Infof("Extraction started: %s", strings.Join(resp.Archives.List(), ", "))

	resp = <-response
	if resp.Error != nil {
		// There is possibly more data in the response that is useful even on error.
		// ie you may want to cleanup any partial extraction.
		log.Printf("Error: %v", resp.Error)
	}

	log.Infof("Extracted Files:\n - %s", strings.Join(resp.NewFiles, "\n - "))
}
```

### Example 2 - Direct

This example shows `ExtractFile()` with a very simple `XFile`.
You can choose output path, as well as file and dir modes.
Failing to provide `OutputDir` results in unexpected behavior.
`ExtractFile()` attempts to identify the type of file. If you
know the file type you may call the direct method instead:

- `ExtractZIP(*XFile)`
- `ExtractRAR(*XFile)`
- `ExtractTar(*XFile)`
- `ExtractGzip(*XFile)`
- `ExtractBzip(*XFile)`
- `ExtractTarGzip(*XFile)`
- `ExtractTarBzip(*XFile)`
- `Extract7z(*XFile)`
- `ExtractISO(*XFile)`
- `SplitCueFlac(*XFile)`
- etc.. there's more than this.

```golang
package main

import (
	"log"
	"strings"

	"golift.io/xtractr"
)

func main() {
	x := &xtractr.XFile{
		FilePath:  "/tmp/myfile.zip",
		OutputDir: "/tmp/myfile", // do not forget this.
	}

	// size is how many bytes were written.
	// files may be nil, but will contain any files written (even with an error).
	size, files, _, err := xtractr.ExtractFile(x)
	if err != nil || files == nil {
		log.Fatal(size, files, err)
	}

	log.Println("Bytes written:", size, "Files Extracted:\n -", strings.Join(files, "\n -"))
}
```

## XFile Input

This is what `XFile` looks like (yesterday at least):

```golang
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
	// Suffix brands cross-device copy siblings as a known extra extension
	// (e.g. movie.mkv.xtractr_partial). Empty uses DefaultSuffix.
	Suffix string
	// (RAR/7z) Archive password. Blank for none. Gets prepended to Passwords, below.
	Password string
	// (RAR/7z) Archive passwords (to try multiple).
	Passwords []string
	// FileWorkers controls how many files within a single archive are extracted
	// concurrently. Only effective for random-access formats (ZIP, 7z).
	// Streaming formats ignore this. 0 or 1 = sequential (current behavior).
	// Total concurrent I/O when using the queue = Config.Parallel * FileWorkers.
	FileWorkers int
	// MaxBytes is the maximum uncompressed bytes written for this archive.
	// 0 means unlimited.
	MaxBytes uint64
	// MaxFiles is the maximum files, directories, and symlinks created for this
	// archive. 0 means unlimited.
	MaxFiles int
	// MaxRatio is the maximum bytesWritten / archiveFileSize. 0 means unlimited.
	MaxRatio float64
	// AllowSymlinks allows FilePath to be a symbolic link to an archive.
	AllowSymlinks bool
	// Progress is called periodically during file extraction.
	// Contains info about the progress of the extraction.
	// This is not called if an Updates channel is also provided.
	Progress func(Progress)
	// If an Updates channel is provided, all Progress updates are sent to it.
	// Contains info about the progress of the extraction.
	Updates chan Progress
	// If the archive only has one directory in the root, then setting
	// this true will cause the extracted content to be moved into the
	// output folder, and the root folder in the archive to be removed.
	SquashRoot bool
	// SkipOnRecursion, if set by an extractor, lists paths that were copied into
	// the output (e.g. a CUE sheet) and must not be re-extracted when recursing.
	SkipOnRecursion []string
}
```

### Input Safety

Per-archive caps on `XFile`, `Xtract`, and `Config` (`0` means unlimited):

- `MaxBytes` — uncompressed bytes written for one archive
- `MaxFiles` — files, directories, and symlinks created for one archive
- `MaxRatio` — `bytesWritten / archiveFileSize`

Queue extras caps on `Xtract` and `Config` (`0` inherits `Config`, then unlimited):

- `MaxNested` — archives extracted from one source folder's extras pass
- `ExtrasMaxDepth` — how deep that extras walk goes

`AllowSymlinks` is on `Filter`/`Xtract` and `XFile` (default false). When set, the initial search may include
symlink-named archives. The extras pass never follows archive-member links.

Exceeding `MaxBytes`/`MaxFiles`/`MaxRatio` returns those errors and stops the extract.
Exceeding `MaxNested` returns `ErrMaxNested` and deletes that folder's extract output.
Queue jobs inherit `Config` values when the `Xtract` field is `0`.

`MaxFiles` counts claimed-plus-created entries, not just created. An archive whose header claims more entries
than `MaxFiles` fails fast with `ErrMaxFiles` even when every entry already exists on disk and nothing new would
be created. This fail-closed default is intentional for a security cap: headers can understate, so the runtime
write path always re-checks regardless of what the header claimed.
