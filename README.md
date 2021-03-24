# `xtractr`

Go Library for Queuing and Extracting ZIP, RAR, GZ, BZ2, TAR, TGZ, TBZ2 files.
Can also be used ad-hoc for direct decompression and extraction. See docs.

-   [GoDoc](https://pkg.go.dev/golift.io/xtractr)


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
// Provides two log streams for errors and debug.
type Logger struct {
	error *log.Logger
	debug *log.Logger
}

// Printf satisfies the xtractr.Logger interface.
func (l *Logger) Printf(msg string, v ...interface{}) {
	l.error.Printf(msg, v...)
}

// Debug satisfies the xtractr.Logger interface.
func (l *Logger) Debugf(msg string, v ...interface{}) {
	l.debug.Printf(msg, v...)
}

func main() {
	q := xtractr.NewQueue(&xtractr.Config{
		Suffix: "_xtractd",
		Logger: &Logger{
			error: log.New(os.Stdout, "[XTRACTR] ", 0),
			debug: log.New(os.Stdout, "[DEBUG] ", 0),
		},
		Parallel: 1,
		FileMode: 0644, // ignored for tar files.
		DirMode:  0755,
	})
	defer q.Stop()

	response := make(chan *xtractr.Response)

	// This sends an item into the extraction queue (buffered channel).
	q.Extract(&xtractr.Xtract{
		Name:       "my archive",    // name is not import to this library.
		SearchPath: "/tmp/archives", // can also be a direct file.
		CBChannel:  response,        // queue responses are sent here.
	})

	log.SetPrefix("[INFO] ")

	// Queue always sends two responses. 1 on start and again when finished (error or not)
	resp := <-response
	log.Println("Extraction started:", strings.Join(resp.Archives, ", "))

	resp = <-response
	if resp.Error != nil {
		// There is possibly more data in the response that is useful even on error.
		// ie you may want to cleanup any partial extraction.
		q.Logger.Printf("Error: %v", resp.Error) // You can use the same logger if you want.
	}

	log.Println("Extracted Files:\n", "-", strings.Join(resp.NewFiles, "\n - "))
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
	Password  string      // (RAR) Archive password. Blank for none.
}
```
