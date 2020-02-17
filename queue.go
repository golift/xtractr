package xtractr

/* This file contains methods that supports the queuing system. */

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// List of supported compression types. This isn't used.
const (
	//	TGZ ExtType = "tgz"
	//	GZP ExtType = "gz"
	//	BZ2 ExtType = "bz2"
	RAR ExtType = "rar"
	ZIP ExtType = "zip"
)

// Xtract defines the data needed to extract a path.
type Xtract struct {
	Name       string          // Unused here, but passed back into callback.
	SearchPath string          // Place to find extractable items.
	TempFolder bool            // Leave files in temporary folder?
	DeleteOrig bool            // Delete Archives? Only works if TempFolder is true.
	CBFunction func(*Response) // Callback Function, run in a go routine.
}

// Response is sent to the call-back function.
// The data is not thread safe until Done=true.
type Response struct {
	Name     string        // Comes from *Extract.
	Done     bool          // Extract Started (false) or Finished (true).
	Error    error         // Error encountered, only when done=true.
	Size     int64         // Size of data written.
	Output   string        // Temporary output folder.
	Queued   int           // Items still in queue.
	Started  time.Time     // When this extract began.
	Elapsed  time.Duration // How long it took.
	Extras   []string      // Extra archives extracted.
	Archives []string      // Archives found and extracted.
	NewFiles []string      // Files written to final path.
	AllFiles []string      // Total files written (temp path).
}

// Extract is how external code begins an extraction process against a path.
func (x *Xtractr) Extract(ex *Xtract) (int, error) {
	if x.queue == nil {
		return -1, fmt.Errorf("extractor queue stopped")
	}

	x.queue <- ex // goes to processQueue()

	return len(x.queue), nil
}

// processQueue runs in a go routine, 'e.Parallel' times,
// and watches for things to extract.
func (x *Xtractr) processQueue() {
	for ex := range x.queue {
		x.extract(ex)
	}
}

// extract is where the real work begins and files get extracted.
// This is fired off from the queue.
func (x *Xtractr) extract(ex *Xtract) {
	re := &Response{
		Name:    ex.Name,
		Started: time.Now(),
		Output:  filepath.Join(ex.SearchPath, x.Suffix),
	}

	re.Archives = FindCompressedFiles(ex.SearchPath)

	if len(re.Archives) < 1 {
		x.finishExtract(ex, re, fmt.Errorf("no compressed files found"))
		return
	}

	if ex.CBFunction != nil {
		re.Queued = len(x.queue)
		ex.CBFunction(re) // This lets the calling function know we've started.
	}

	// e.log("Starting: %d archives - %v", len(resp.Archives), ex.SearchPath)
	x.finishExtract(ex, re, x.decompressFiles(ex, re))
}

func (x *Xtractr) finishExtract(ex *Xtract, re *Response, err error) {
	if ex.CBFunction == nil {
		// log something.
		return
	}

	re.Error = err
	re.Elapsed = time.Since(re.Started)
	re.Done = true
	re.Queued = len(x.queue)
	ex.CBFunction(re) // This lets the calling function know we've finished.
}

// decompressFiles runs after we find and verify archives exist.
func (x *Xtractr) decompressFiles(ex *Xtract, re *Response) error {
	err := os.MkdirAll(re.Output, 0755)
	if err != nil {
		return err
	}

	re.Extras, re.Size, re.AllFiles, err = x.processArchives(re.Output, re.Archives)
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(re.Output, x.Suffix)
	msg := fmt.Sprintf("%s - this file is removed with the extracted data\n"+
		"from: %s\npath: %s\ntime: %v\nfiles:\n%v\n%v\n", x.Suffix,
		ex.SearchPath, re.Output, time.Now(), tmpFile, strings.Join(re.NewFiles, "\n"))

	if err := ioutil.WriteFile(tmpFile, []byte(msg), 0744); err != nil {
		x.log("Error: Creating Temporary Tracking File: %v", err)
	}

	// Add the file we just wrote to the list of files written.
	re.NewFiles = append(x.GetFileList(re.Output), tmpFile)

	if ex.DeleteOrig {
		x.deleteFiles(re.Archives...) // as requested
	}

	if !ex.TempFolder {
		// Move the extracted files back into their original folder.
		re.NewFiles, err = x.MoveFiles(re.Output, ex.SearchPath)
		if err != nil {
			if !ex.DeleteOrig {
				// cleanup the broken decompression, but only if we didn't delete the originals.
				x.deleteFiles(re.Output)
			}

			return err
		}
	}

	return nil
}

// Extract one archive at a time, then check if it contained any more archives.
// Returns list of extra files extracted, size of data written, files written.
func (x *Xtractr) processArchives(tmpPath string, archives []string) ([]string, int64, []string, error) {
	files, extras := []string{}, []string{}
	size := int64(0)

	for _, filename := range archives {
		x.debug("Extracting File: %v", filename)
		beforeFiles := x.GetFileList(tmpPath) // get the "before this extraction" file list
		ss, ff, err := ExtractFile(filename, tmpPath)
		files = append(files, ff...)
		size += ss

		if err != nil {
			x.deleteFiles(tmpPath)
			return extras, size, files, err
		}

		newFiles := Difference(beforeFiles, x.GetFileList(tmpPath))

		// Check if we just extracted more archives.
		for _, filename := range newFiles {
			if strings.HasSuffix(filename, ".rar") || strings.HasSuffix(filename, ".zip") {
				// recurse and append data to tracking vars.
				ee, ss, ff, err := x.processArchives(tmpPath, []string{filename})
				extras = append(append(extras, ee...), filename)
				files = append(files, ff...)
				size += ss

				if err != nil {
					return extras, size, files, err
				}
			}
		}
	}

	return extras, size, files, nil
}
