# `xtractr`

Go Library for Queuing and Extracting ZIP, RAR, GZ, BZ2, TAR, 
TGZ, TBZ2, 7Z, ISO ([and other](https://github.com/golift/xtractr/issues/44)) compressed archive files.
Can also be used ad-hoc for direct decompression and extraction. See docs.

-   [GoDoc](https://pkg.go.dev/golift.io/xtractr)
-   Works on Linux, Windows, FreeBSD and macOS **without Cgo**.
-   Supports 32 and 64 bit architectures.
-   Decrypts RAR and 7-Zip archives with passwords.

# Interface

This library provides a queue, and a common interface to extract files.
It does not do the heavy lifting, and relies on these libraries to extract files:

- [**RAR**: nwaples/rardecode](https://github.com/nwaples/rardecode)
- [**7-Zip**: bodgit/sevenzip](https://github.com/bodgit/sevenzip)
- [**ISO**: kdomanski/iso9660](https://github.com/kdomanski/iso9660)
- [**Brotli**: andybalholm/brotli](https://github.com/andybalholm/brotli)
- [**LZ4**: pierrec/lz4](https://github.com/pierrec/lz4)
- [**XZ**: github.com/therootcompany/xz](https://github.com/github.com/therootcompany/xz)
- [**Zstandard**: klauspost/compress](https://github.com/klauspost/compress)
- [**S2**: klauspost/compress](https://github.com/klauspost/compress)
- [**Snappy**: klauspost/compress](https://github.com/klauspost/compress)
- [**Zlib**: klauspost/compress](https://github.com/klauspost/compress)
- [**LZW**: sshaman1101/dcompress](https://github.com/sshaman1101/dcompress)

`Zip`, `Gzip`, `Tar` and `Bzip` are all handled by the standard Go library.

# Examples

## Example 1 - Queue

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
		Name:       "my archive",    // name is not import to this library.
		SearchPath: "/tmp/archives", // can also be a direct file.
		CBChannel:  response,        // queue responses are sent here.
	})

	// Queue always sends two responses. 1 on start and again when finished (error or not)
	resp := <-response
	log.Infof("Extraction started: %s", strings.Join(resp.Archives, ", "))

	resp = <-response
	if resp.Error != nil {
		// There is possibly more data in the response that is useful even on error.
		// ie you may want to cleanup any partial extraction.
		log.Printf("Error: %v", resp.Error)
	}

	log.Infof("Extracted Files:\n - %s", strings.Join(resp.NewFiles, "\n - "))
}
```

## Example 2 - Direct

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
	size, files, err := xtractr.ExtractFile(x)
	if err != nil || files == nil {
		log.Fatal(size, files, err)
	}

	log.Println("Bytes written:", size, "Files Extracted:\n -", strings.Join(files, "\n -"))
}
```

This is what `XFile` looks like (today at least):
```golang
// XFile defines the data needed to extract an archive.
type XFile struct {
	FilePath  string      // Path to archive being extracted.
	OutputDir string      // Folder to extract archive into.
	FileMode  os.FileMode // Write files with this mode.
	DirMode   os.FileMode // Write folders with this mode.
	Password  string      // (RAR/7z) Archive password. Blank for none.
}
```
