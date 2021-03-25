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
	Password   string          // Archive password. Only supported with RAR files.
	SearchPath string          // Folder path where extractable items are located.
	ExtractTo  string          // Default is same level as SearchPath with a suffix.
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

	x.done <- struct{}{}
}

// extract is where the real work begins and files get extracted.
// This is fired off from processQueue() in a go routine.
func (x *Xtractr) extract(ex *Xtract) {
	re := &Response{
		X:        ex,
		Started:  time.Now(),
		Output:   strings.TrimRight(ex.SearchPath, `/\`) + x.config.Suffix, // tmp folder.
		Archives: FindCompressedFiles(ex.SearchPath),
		Queued:   len(x.queue),
	}

	if ex.ExtractTo != "" {
		re.Output = filepath.Join(ex.ExtractTo, filepath.Base(re.Output))
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
		x.config.Printf("Error Extracting: %s (%v elapsed): %v", re.X.SearchPath, re.Elapsed, err)

		return
	}

	x.config.Printf("Finished Extracting: %s (%v elapsed, queue size: %d)", re.X.SearchPath, re.Elapsed, re.Queued)
}

// decompressFiles runs after we find and verify archives exist.
// This extracts everything in the search path then checks the
// output path for more archives that were just decompressed.
func (x *Xtractr) decompressFiles(re *Response) error {
	if err := x.decompressArchives(re); err != nil {
		return err
	}

	// Now do it again with the output folder.
	re.Extras = FindCompressedFiles(re.Output)
	nre := &Response{
		X:        &Xtract{Password: re.X.Password},
		Started:  re.Started,
		Output:   re.Output,
		Archives: re.Extras,
	}
	err := x.decompressArchives(nre)
	// Combine the new Response with the existing response.
	re.Extras = nre.Archives
	re.Size += nre.Size

	if nre.NewFiles != nil {
		re.NewFiles = append(re.NewFiles, nre.NewFiles...)
	}

	if err != nil {
		return err
	}

	return x.cleanupProcessedArchives(re)
}

func (x *Xtractr) decompressArchives(re *Response) error {
	allArchives := []string{}

	for _, archive := range re.Archives {
		bytes, files, archives, err := x.processArchive(archive, re.Output, re.X.Password)
		// Make sure these get added even with an error.
		if re.Size += bytes; files != nil {
			re.NewFiles = append(re.NewFiles, files...)
		}

		if len(archives) != 0 {
			allArchives = append(allArchives, archives...)
		}

		if err != nil {
			return err
		}
	}

	re.Archives = allArchives

	return nil
}

// processArchives extracts one archive at a time.
// Returns list of archive files extracted, size of data written and files written.
func (x *Xtractr) processArchive(filename, tmpPath, password string) (int64, []string, []string, error) {
	if err := os.MkdirAll(tmpPath, x.config.DirMode); err != nil {
		return 0, nil, nil, fmt.Errorf("os.MkdirAll: %w", err)
	}

	x.config.Debugf("Extracting File: %v to %v", filename, tmpPath)

	bytes, files, archives, err := ExtractFile(&XFile{ // extract the file.
		FilePath:  filename,
		OutputDir: tmpPath,
		FileMode:  x.config.FileMode,
		DirMode:   x.config.DirMode,
		Password:  password,
	})
	if err != nil {
		x.DeleteFiles(tmpPath) // clean up the mess after an error and bail.
	}

	return bytes, files, archives, err
}

func (x *Xtractr) cleanupProcessedArchives(re *Response) error {
	tmpFile := filepath.Join(re.Output, x.config.Suffix+"."+filepath.Base(re.X.SearchPath)+".txt")
	re.NewFiles = append(re.NewFiles, tmpFile)

	msg := []byte(fmt.Sprintf("# %s - this file is removed with the extracted data\n---\n"+
		"archives:%s\nextras:%v\nfrom_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n",
		x.config.Suffix, re.Archives, re.Extras, re.X.SearchPath, re.Output, !re.X.TempFolder, time.Now(),
		strings.Join(re.NewFiles, "\n  - ")))

	err := ioutil.WriteFile(tmpFile, msg, x.config.FileMode)
	if err != nil {
		x.config.Printf("Error: Creating Temporary Tracking File: %v", err) // continue anyway.
	}

	if re.X.DeleteOrig {
		x.DeleteFiles(re.Archives...) // as requested

		if len(re.Extras) != 0 {
			x.DeleteFiles(re.Extras...) // these got extracted too
		}
	}

	// If TempFolder is false then move the files back to the original location.
	if !re.X.TempFolder {
		re.NewFiles, err = x.MoveFiles(re.Output, re.X.SearchPath, false)
	} else if len(x.GetFileList(re.X.SearchPath)) == 0 {
		// If the original path is empty, delete it.
		x.DeleteFiles(re.X.SearchPath)
	}

	return err
}
