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
	Name       string          // Unused in this app; exposed for calling library.
	SearchPath string          // Folder path where extractable items are located.
	TempFolder bool            // Leave files in temporary folder? false=move files back to Searchpath
	DeleteOrig bool            // Delete Archives after successful extraction? Be careful.
	CBFunction func(*Response) // Callback Function, runs twice per queued item.
	CBChannel  chan *Response  // Callback Channel, msg sent twice per queued item.
}

// Response is sent to the call-back function. The first CBFunction call is just
// a notification that the extraction has started. You can determine it's the first
// call by chcking Response.Done. false = started, true = finished. When done=false
// the only other meaningful data provided is the re.Archives, re.Output and re.Queue.
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
	Error    error         // Error encountered, only when done=true.
	X        *Xtract       // Copied from input data.
}

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
	for ex := range x.queue { // extractions come from Extract()
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

	// Create another pointer to avoid race conditions in the callbacks above.
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
		x.Printf("Error Extracting: %s (%v elapsed): %v", re.X.SearchPath, re.Elapsed, err)

		return
	}

	x.Printf("Finished Extracting: %s (%v elapsed, queue size: %d)", re.X.SearchPath, re.Elapsed, re.Queued)
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

func (x *Xtractr) cleanupProcessedArchive(re *Response, archivePath string) error {
	tmpFile := filepath.Join(re.Output, x.Suffix+"."+filepath.Base(archivePath)+".txt")
	re.NewFiles = append(x.GetFileList(re.Output), tmpFile)

	msg := []byte(fmt.Sprintf("# %s - this file is removed with the extracted data\n---\n"+
		"archive:%s\nextras:%v\nfrom_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n",
		x.Suffix, archivePath, re.Extras, re.X.SearchPath, re.Output, !re.X.TempFolder, time.Now(),
		strings.Join(re.NewFiles, "\n  - ")))

	if err := ioutil.WriteFile(tmpFile, msg, x.FileMode); err != nil {
		x.Printf("Error: Creating Temporary Tracking File: %v", err) // continue anyway.
	}

	if re.X.DeleteOrig {
		x.DeleteFiles(archivePath) // as requested
	}

	var err error
	// Only move back the files if the archive wasn't extracted from the temp path.
	if archiveDir := filepath.Dir(archivePath); !re.X.TempFolder && re.Output != archiveDir {
		// Move the extracted files back into the same folder as the archive.
		re.NewFiles, err = x.MoveFiles(re.Output, archiveDir, false)
	}

	return err
}

// processArchives extracts one archive at a time, then checks if it extracted more archives.
// Returns list of extra files extracted, size of data written and files written.
func (x *Xtractr) processArchive(filename string, tmpPath string) (*Response, error) {
	output := &Response{NewFiles: []string{}, Extras: []string{}}

	x.Debugf("Extracting File: %v to %v", filename, tmpPath)
	beforeFiles := x.GetFileList(tmpPath)    // get the "before this extraction" file list
	bytes, files, err := ExtractFile(&XFile{ // extract the file.
		FilePath:  filename,
		OutputDir: tmpPath,
		FileMode:  x.FileMode,
		DirMode:   x.DirMode,
	})
	output.NewFiles = append(output.NewFiles, files...) // keep track of the files extracted.
	output.Size += bytes                                // total the size of data written.

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
			output.NewFiles = append(output.NewFiles, o.NewFiles...)             // keep track of the files extracted.
			output.Size += o.Size                                                // total the size of data written.

			if err != nil {
				return output, err
			}
		}
	}

	return output, nil
}
