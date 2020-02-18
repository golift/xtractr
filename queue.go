package xtractr

/* This file contains methods that support the extract queuing system. */

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ExtType represents a supported compression scheme.
// Use this to choose which types of files to find for extraction.
type ExtType string

// List of supported compression types. This isn't used (yet).
const (
	//	TGZ ExtType = ".tgz"
	//	GZP ExtType = ".gz"
	//	BZ2 ExtType = ".bz2"
	RAR ExtType = ".rar"
	ZIP ExtType = ".zip"
)

// Xtract defines the queue input data: data needed to extract files in a path.
// Fill this out to create a queued extraction and pass it into Xtractr.Extract().
// If a CBFunction is provided it runs when the queued extract begins w/ Response.Done=false.
// The CBFunction is called again when the extraction finishes w/ Response.Done=true.
type Xtract struct {
	Name       string          // Unused in this app; exposed for calling library.
	SearchPath string          // Folder path where extractable items are located.
	TempFolder bool            // Leave files in temporary folder? false=move files back to Searchpath
	DeleteOrig bool            // Delete Archives after successful extraction? Be careful.
	CBFunction func(*Response) // Callback Function, runs twice per queued item.
	FindFileEx []ExtType       // UNUSED (yet). Archive types to find in SearchPath. nil=ALL TYPES
}

// Response is sent to the call-back function. The first CBFunction call is just
// a notification that the extraction has started. You can determine it's the first
// call by chcking Response.Done. false = started, true = finished. The data is
// not thread safe until Done=true. When done=false the only other meaningful
// data provided is the Response.Archives, Response.Output and Response.Queue.
type Response struct {
	Done     bool          // Extract Started (false) or Finished (true).
	Size     int64         // Size of data written.
	Output   string        // Temporary output folder.
	Queued   int           // Items still in queue.
	Started  time.Time     // When this extract began.
	Elapsed  time.Duration // Elapsed extraction duration. ie. How long it took.
	Extras   []string      // Extra archives extracted from within an archive.
	Archives []string      // Initial archives found and extracted.
	NewFiles []string      // Files written to final path.
	AllFiles []string      // All (recursive) files written to the temp path.
	Error    error         // Error encountered, only when done=true.
	X        *Xtract       // Copied from input data.
}

// Extract is how external code begins an extraction process against a path.
// To add an item to the extraction queue, create an Xtract struct with the
// search path set and pass it to this method. The current queue size is returned.
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
// This is fired off from processQueue() in a go routine.
func (x *Xtractr) extract(ex *Xtract) {
	re := &Response{
		X:       ex,
		Started: time.Now(),
		Output:  strings.TrimSuffix(ex.SearchPath, "/") + x.Suffix, // tmp folder.
	}

	re.Archives = FindCompressedFiles(ex.SearchPath)

	if len(re.Archives) < 1 {
		x.finishExtract(re, fmt.Errorf("no compressed files found"))
		return
	}

	if ex.CBFunction != nil {
		re.Queued = len(x.queue)
		ex.CBFunction(re) // This lets the calling function know we've started.
	}

	// e.log("Starting: %d archives - %v", len(resp.Archives), ex.SearchPath)
	x.finishExtract(re, x.decompressFiles(re))
}

func (x *Xtractr) finishExtract(re *Response, err error) {
	re.Error = err
	re.Elapsed = time.Since(re.Started)
	re.Done = true
	re.Queued = len(x.queue)

	if re.X.CBFunction == nil {
		if err == nil {
			x.log("Finished Extracting: %s (%v elapsed, still in queue: %d items)",
				re.X.SearchPath, re.Elapsed, re.Queued) // log something better.
		} else {
			x.log("Error Extracting: %s (%v elapsed): %v",
				re.X.SearchPath, re.Elapsed, re.Error)
		}

		return
	}

	re.X.CBFunction(re) // This lets the calling function know we've finished.
}

// decompressFiles runs after we find and verify archives exist.
func (x *Xtractr) decompressFiles(re *Response) error {
	err := os.MkdirAll(re.Output, 0755)
	if err != nil {
		return err
	}

	re.Extras, re.Size, re.AllFiles, err = x.processArchives(re.Archives, re.Output)
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(re.Output, x.Suffix)
	re.NewFiles = append(x.GetFileList(re.Output), tmpFile)

	msg := fmt.Sprintf("# %s - this file is removed with the extracted data\n---\n"+
		"from_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n", x.Suffix,
		re.X.SearchPath, re.Output, !re.X.TempFolder, time.Now(), strings.Join(re.NewFiles, "\n  - "))

	if err := ioutil.WriteFile(tmpFile, []byte(msg), 0744); err != nil {
		x.log("Error: Creating Temporary Tracking File: %v", err) // continue anyway.
	}

	if re.X.DeleteOrig {
		x.DeleteFiles(re.Archives...) // as requested
	}

	if !re.X.TempFolder {
		// Move the extracted files back into their original folder.
		re.NewFiles, err = x.MoveFiles(re.Output, re.X.SearchPath)
		if err != nil {
			if !re.X.DeleteOrig {
				// cleanup the broken decompression, but only if we didn't delete the originals.
				x.DeleteFiles(re.Output)
			}

			return err
		}
	}

	return nil
}

// processArchives extractx one archive at a time, then checks if it extracted more archives.
// Returns list of extra files extracted, size of data written and files written.
func (x *Xtractr) processArchives(archives []string, tmpPath string) ([]string, int64, []string, error) {
	files, extras := []string{}, []string{}
	size := int64(0)

	for _, filename := range archives {
		x.debug("Extracting File: %v", filename)
		beforeFiles := x.GetFileList(tmpPath)         // get the "before this extraction" file list
		ss, ff, err := ExtractFile(filename, tmpPath) // extract the file.
		files = append(files, ff...)                  // keep track of the files extract.
		size += ss                                    // total the size of data written.

		if err != nil {
			x.DeleteFiles(tmpPath) // clean up the mess after an error and bail.
			return extras, size, files, err
		}

		// Check if we just extracted more archives.
		newFiles := Difference(beforeFiles, x.GetFileList(tmpPath))
		for _, filename := range newFiles {
			if strings.HasSuffix(filename, ".rar") || strings.HasSuffix(filename, ".zip") {
				// recurse and append data to tracking vars.
				ee, ss, ff, err := x.processArchives([]string{filename}, tmpPath)
				extras = append(append(extras, ee...), filename) // MORE archives!
				files = append(files, ff...)                     // keep track of the files extract.
				size += ss                                       // total the size of data written.

				if err != nil {
					return extras, size, files, err
				}
			}
		}
	}

	return extras, size, files, nil
}
