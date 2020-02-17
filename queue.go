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

// ExtType represents a supported compression scheme. Use this to pick and
// choose which types of files to find for extraction.
type ExtType string

// List of supported compression types. This isn't used (yet)
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
	Name       string          // Unused here, but passed back into callback.
	SearchPath string          // Path to folder where extractable items are located.
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
	Name     string        // Comes from *Xtract; use this to track your queued Xtract.
	Done     bool          // Extract Started (false) or Finished (true).
	Error    error         // Error encountered, only when done=true.
	Size     int64         // Size of data written.
	Output   string        // Temporary output folder.
	Queued   int           // Items still in queue.
	Started  time.Time     // When this extract began.
	Elapsed  time.Duration // Elapsed extraction duration. ie. How long it took.
	Extras   []string      // Extra archives extracted from within an archive.
	Archives []string      // Initial archives found and extracted.
	NewFiles []string      // Files written to final path.
	AllFiles []string      // All (recursive) files written to the temp path.
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
	re.Error = err
	re.Elapsed = time.Since(re.Started)
	re.Done = true
	re.Queued = len(x.queue)

	if ex.CBFunction == nil {
		if err == nil {
			x.log("Finished Extracting: %s (%v elapsed, still in queue: %d items)",
				ex.SearchPath, re.Elapsed, re.Queued) // log something better.
		} else {
			x.log("Error Extracting: %s (%v elapsed): %v",
				ex.SearchPath, re.Elapsed, re.Error)
		}

		return
	}

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

// processArchives extractx one archive at a time, then checks if it extracted more archives.
// Returns list of extra files extracted, size of data written and files written.
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
