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

// Xtract defines the queue input data: data needed to extract files in a path.
// Fill this out to create a queued extraction and pass it into Xtractr.Extract().
// If a CBFunction is provided it runs when the queued extract begins w/ Response.Done=false.
// The CBFunction is called again when the extraction finishes w/ Response.Done=true.
// The CBFunction channel works the exact same way, except it's a channel instead of a blocking function.
type Xtract struct {
	Name       string          `json:"name"`        // Unused in this app; exposed for calling library.
	SearchPath string          `json:"search_path"` // Folder path where extractable items are located.
	TempFolder bool            `json:"temp_folder"` // Leave files in temporary folder? false=move files back to Searchpath
	DeleteOrig bool            `json:"delete_orig"` // Delete Archives after successful extraction? Be careful.
	CBFunction func(*Response) `json:"-"`           // Callback Function, runs twice per queued item.
	CBChannel  chan *Response  `json:"-"`           // Callback Channel, msg sent twice per queued item.
}

// Response is sent to the call-back function. The first CBFunction call is just
// a notification that the extraction has started. You can determine it's the first
// call by chcking Response.Done. false = started, true = finished. When done=false
// the only other meaningful data provided is the re.Archives, re.Output and re.Queue.
type Response struct {
	Done     bool          `json:"done"`       // Extract Started (false) or Finished (true).
	Size     int64         `json:"bytes"`      // Size of data written.
	Output   string        `json:"tmp_folder"` // Temporary output folder.
	Queued   int           `json:"queue_size"` // Items still in queue.
	Started  time.Time     `json:"start"`      // When this extract began.
	Elapsed  time.Duration `json:"elapsed"`    // Elapsed extraction duration. ie. How long it took.
	Extras   []string      `json:"extras"`     // Extra archives extracted from within an archive.
	Archives []string      `json:"archives"`   // Initial archives found and extracted.
	NewFiles []string      `json:"new_files"`  // Files written to final path.
	AllFiles []string      `json:"all_files"`  // All (recursive) files written to the temp path.
	Error    error         `json:"error"`      // Error encountered, only when done=true.
	X        *Xtract       `json:"input"`      // Copied from input data.
}

var (
	ErrQueueStopped      = fmt.Errorf("extractor queue stopped")
	ErrNoCompressedFiles = fmt.Errorf("no compressed files found")
)

// Extract is how external code begins an extraction process against a path.
// To add an item to the extraction queue, create an Xtract struct with the
// search path set and pass it to this method. The current queue size is returned.
func (x *Xtractr) Extract(ex *Xtract) (int, error) {
	if x.queue == nil {
		return -1, ErrQueueStopped
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
		X:        ex,
		Started:  time.Now(),
		Output:   strings.TrimRight(ex.SearchPath, `/\`) + x.Suffix, // tmp folder.
		Archives: FindCompressedFiles(ex.SearchPath),
		Queued:   len(x.queue),
	}

	if len(re.Archives) < 1 { // no archives to xtract, bail out.
		x.finishExtract(re, ErrNoCompressedFiles)

		return
	}

	if ex.CBFunction != nil {
		ex.CBFunction(re) // This lets the calling function know we've started.
	}

	if ex.CBChannel != nil {
		ex.CBChannel <- re // This lets the calling function know we've started.
	}
	// Create another pointer to avoid race conditions in the callback function above.
	re = &Response{X: ex, Started: re.Started, Output: re.Output, Archives: re.Archives}
	// e.log("Starting: %d archives - %v", len(resp.Archives), ex.SearchPath)
	x.finishExtract(re, x.decompressFiles(re))
}

func (x *Xtractr) finishExtract(re *Response, err error) {
	re.Error = err
	re.Elapsed = time.Since(re.Started)
	re.Done = true
	re.Queued = len(x.queue)

	if re.X.CBFunction != nil {
		re.X.CBFunction(re) // This lets the calling function know we've finished.
	}

	if re.X.CBChannel != nil {
		re.X.CBChannel <- re // This lets the calling function know we've finished.
	}

	if re.X.CBChannel != nil || re.X.CBFunction != nil {
		return
	}

	// Only print a message if there is no callback function. Allows apps to print their own messages.
	if err != nil {
		x.log("Error Extracting: %s (%v elapsed): %v", re.X.SearchPath, re.Elapsed, err)

		return
	}

	x.log("Finished Extracting: %s (%v elapsed, queue size: %d)", re.X.SearchPath, re.Elapsed, re.Queued)
}

// decompressFiles runs after we find and verify archives exist.
func (x *Xtractr) decompressFiles(re *Response) error {
	err := os.MkdirAll(re.Output, x.DirMode)
	if err != nil {
		return fmt.Errorf("os.MkdirAll: %w", err)
	}

	for _, archive := range re.Archives {
		// 'o' is the response for _this_ archive file, 're' is the whole batch.
		o, err := x.processArchive(archive, re.Output)

		if len(o.Extras) > 0 {
			re.Extras = append(re.Extras, o.Extras...)
		}

		re.Size += o.Size

		if err != nil {
			// Make sure these get added in case there is an error.
			// If there is no error, we add a different set later.
			if len(o.NewFiles) > 0 {
				re.NewFiles = append(re.NewFiles, o.NewFiles...)
			}

			return err
		}

		o.Output, o.X = re.Output, re.X

		err = x.cleanupProcessedArchive(o, archive)

		if len(o.NewFiles) > 0 {
			// Append any new files, even if there was an error.
			re.NewFiles = append(re.NewFiles, o.NewFiles...)
		}

		if err != nil {
			return err
		}
	}

	return nil
}

func (x *Xtractr) cleanupProcessedArchive(re *Response, archive string) error {
	tmpFile := filepath.Join(re.Output, x.Suffix)
	re.NewFiles = append(x.GetFileList(re.Output), tmpFile)
	msg := fmt.Sprintf("# %s - this file is removed with the extracted data\n---\n"+
		"archive:%s\nextras:%v\nfrom_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n",
		x.Suffix, archive, re.Extras, re.X.SearchPath, re.Output, !re.X.TempFolder, time.Now(), strings.Join(re.NewFiles, "\n  - "))

	err := ioutil.WriteFile(tmpFile, []byte(msg), x.FileMode)
	if err != nil {
		x.log("Error: Creating Temporary Tracking File: %v", err) // continue anyway.
	}

	if re.X.DeleteOrig {
		x.DeleteFiles(archive) // as requested
	}

	if !re.X.TempFolder {
		// Move the extracted files back into the same folder as the archive.
		re.NewFiles, err = x.MoveFiles(re.Output, filepath.Base(archive), false)
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

// processArchives extracts one archive at a time, then checks if it extracted more archives.
// Returns list of extra files extracted, size of data written and files written.
func (x *Xtractr) processArchive(filename string, tmpPath string) (*Response, error) {
	output := &Response{NewFiles: []string{}, Extras: []string{}}

	x.debug("Extracting File: %v to %v", filename, tmpPath)
	beforeFiles := x.GetFileList(tmpPath) // get the "before this extraction" file list
	ss, ff, err := ExtractFile(&XFile{    // extract the file.
		FilePath:  filename,
		OutputDir: tmpPath,
		FileMode:  x.FileMode,
		DirMode:   x.DirMode,
	})
	output.NewFiles = append(output.NewFiles, ff...) // keep track of the files extracted.
	output.Size += ss                                // total the size of data written.

	if err != nil {
		x.DeleteFiles(tmpPath) // clean up the mess after an error and bail.

		return output, err
	}

	// Check if we just extracted more archives.
	newFiles := Difference(beforeFiles, x.GetFileList(tmpPath))
	for _, filename := range newFiles {
		if strings.HasSuffix(filename, ".rar") || strings.HasSuffix(filename, ".zip") {
			// recurse and append data to tracking vars.
			o, err := x.processArchive(filename, tmpPath)
			output.Extras = append(append(output.Extras, o.Extras...), filename) // MORE archives!
			output.NewFiles = append(output.NewFiles, o.NewFiles...)             // keep track of the files extract.
			output.Size += o.Size                                                // total the size of data written.

			if err != nil {
				return output, err
			}
		}
	}

	return output, nil
}
