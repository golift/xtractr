package xtractr

/* This file contains methods that support the extract queuing system. */

import (
	"fmt"
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
	Passwords  []string        // Archive passwords (try multiple). Only supported with RAR files.
	SearchPath string          // Folder path where extractable items are located.
	ExtractTo  string          // Default is same level as SearchPath with a suffix.
	TempFolder bool            // Leave files in temporary folder? false=move files back to Searchpath
	DeleteOrig bool            // Delete Archives after successful extraction? Be careful.
	LogFile    bool            // Create a log (.txt) file of the extraction information.
	CBFunction func(*Response) // Callback Function, runs twice per queued item.
	CBChannel  chan *Response  // Callback Channel, msg sent twice per queued item.
}

// Response is sent to the call-back function. The first CBFunction call is just
// a notification that the extraction has started. You can determine it's the first
// call by chcking Response.Done. false = started, true = finished. When done=false
// the only other meaningful data provided is the re.Archives, re.Output and re.Queue.
type Response struct {
	Done     bool                // Extract Started (false) or Finished (true).
	Size     int64               // Size of data written.
	Output   string              // Temporary output folder.
	Queued   int                 // Items still in queue.
	Started  time.Time           // When this extract began.
	Elapsed  time.Duration       // Elapsed extraction duration. ie. How long it took.
	Extras   map[string][]string // Extra archives extracted from within an archive.
	Archives map[string][]string // Initial archives found and extracted.
	NewFiles []string            // Files written to final path.
	Error    error               // Error encountered, only when done=true.
	X        *Xtract             // Copied from input data.
}

// Extract is how external code begins an extraction process against a path.
// To add an item to the extraction queue, create an Xtract struct with the
// search path set and pass it to this method. The current queue size is returned.
func (x *Xtractr) Extract(extract *Xtract) (int, error) {
	if x.queue == nil {
		return -1, ErrQueueStopped
	}

	queueSize := len(x.queue) + 1
	x.queue <- extract // goes to processQueue()

	return queueSize, nil
}

// processQueue runs in a go routine, 'x.Parallel' times,
// and watches for things to extract.
func (x *Xtractr) processQueue() {
	for ex := range x.queue { // extractions come from Extract()
		x.extract(ex)
	}

	x.done <- struct{}{}
}

// extract is where the real work begins and files get extracted.
// This is fired off from processQueue() in a go routine.
func (x *Xtractr) extract(ext *Xtract) {
	resp := &Response{
		X:        ext,
		Started:  time.Now(),
		Output:   strings.TrimRight(ext.SearchPath, `/\`) + x.config.Suffix, // tmp folder.
		Archives: FindCompressedFiles(ext.SearchPath),
		Queued:   len(x.queue),
	}

	if ext.ExtractTo != "" {
		resp.Output = filepath.Join(ext.ExtractTo, filepath.Base(resp.Output))
	}

	if len(resp.Archives) < 1 { // no archives to xtract, bail out.
		x.finishExtract(resp, ErrNoCompressedFiles)

		return
	}

	if ext.CBFunction != nil {
		ext.CBFunction(resp) // This lets the calling function know we've started.
	}

	if ext.CBChannel != nil {
		ext.CBChannel <- resp // This lets the calling function know we've started.
	}

	// Create another pointer to avoid race conditions in the callbacks above.
	resp2 := &Response{
		X:        ext,
		Started:  resp.Started,
		Output:   resp.Output,
		Archives: make(map[string][]string),
		Extras:   make(map[string][]string),
	}

	for k, v := range resp.Archives {
		resp2.Archives[k] = append(resp2.Archives[k], v...)
	}

	// e.log("Starting: %d archives - %v", len(resp.Archives), ex.SearchPath)
	x.finishExtract(resp2, x.decompressFolders(resp2))
}

// decompressFolders extracts each folder individually,
// or the extracted files may be copied back to where they were extracted from.
// If the extracted data is not being coppied back, then the tempDir (output) paths match the input paths.
func (x *Xtractr) decompressFolders(resp *Response) error {
	allArchives := make(map[string][]string)

	for subDir := range resp.Archives {
		subResp := &Response{
			X: &Xtract{
				SearchPath: subDir,
				Name:       resp.X.Name,
				Password:   resp.X.Password,
				Passwords:  resp.X.Passwords,
				ExtractTo:  resp.X.ExtractTo,
				DeleteOrig: resp.X.DeleteOrig,
				TempFolder: resp.X.TempFolder,
				LogFile:    resp.X.LogFile,
			},
			Started:  resp.Started,
			Output:   filepath.Join(resp.Output, strings.TrimPrefix(subDir, resp.X.SearchPath)),
			Archives: map[string][]string{subDir: resp.Archives[subDir]},
		}

		err := x.decompressFiles(subResp)
		resp.NewFiles = append(resp.NewFiles, subResp.NewFiles...)
		resp.Size += subResp.Size

		if err != nil {
			return err
		}

		for k, v := range subResp.Extras {
			resp.Extras[k] = append(resp.Extras[k], v...)
		}

		for k, v := range subResp.Archives {
			allArchives[k] = append(allArchives[k], v...)
		}
	}

	resp.Archives = allArchives

	return nil
}

func (x *Xtractr) finishExtract(resp *Response, err error) {
	if resp.X.TempFolder {
		x.cleanTempFolder(resp)
	}

	resp.Error = err
	resp.Elapsed = time.Since(resp.Started)
	resp.Done = true
	resp.Queued = len(x.queue)

	if resp.X.CBFunction != nil {
		resp.X.CBFunction(resp) // This lets the calling function know we've finished.
	}

	if resp.X.CBChannel != nil {
		resp.X.CBChannel <- resp // This lets the calling function know we've finished.
	}

	if resp.X.CBChannel != nil || resp.X.CBFunction != nil {
		return
	}

	// Only print a message if there is no callback function. Allows apps to print their own messages.
	if err != nil {
		x.config.Printf("Error Extracting: %s (%v elapsed): %v", resp.X.SearchPath, resp.Elapsed, err)
		return
	}

	x.config.Printf("Finished Extracting: %s (%v elapsed, queue size: %d)", resp.X.SearchPath, resp.Elapsed, resp.Queued)
}

// decompressFiles runs after we find and verify archives exist.
// This extracts everything in the search path then checks the
// output path for more archives that were just decompressed.
func (x *Xtractr) decompressFiles(resp *Response) error {

	if err := x.decompressArchives(resp); err != nil {
		return err
	}

	// Now do it again with the output folder.
	resp.Extras = FindCompressedFiles(resp.Output)
	nre := &Response{
		X: &Xtract{
			Password:  resp.X.Password,
			Passwords: resp.X.Passwords,
		},
		Started:  resp.Started,
		Output:   resp.Output,
		Archives: resp.Extras,
	}
	err := x.decompressArchives(nre)
	// Combine the new Response with the existing response.
	resp.Extras = nre.Archives
	resp.Size += nre.Size

	if nre.NewFiles != nil {
		resp.NewFiles = append(resp.NewFiles, nre.NewFiles...)
	}

	if err != nil {
		return err
	}

	return x.cleanupProcessedArchives(resp)
}

func (x *Xtractr) decompressArchives(resp *Response) error {
	for parentDir, archives := range resp.Archives {
		allArchives := []string{}

		for _, archive := range archives {
			bytes, files, archives, err := x.processArchive(archive, resp)
			// Make sure these get added even with an error.
			if resp.Size += bytes; files != nil {
				resp.NewFiles = append(resp.NewFiles, files...)
			}

			if len(archives) != 0 {
				allArchives = append(allArchives, archives...)
			}

			if err != nil {
				return err
			}
		}

		resp.Archives[parentDir] = allArchives
	}

	return nil
}

// processArchives extracts one archive at a time.
// Returns list of archive files extracted, size of data written and files written.
func (x *Xtractr) processArchive(filename string, resp *Response) (int64, []string, []string, error) {
	if err := os.MkdirAll(resp.Output, x.config.DirMode); err != nil {
		return 0, nil, nil, fmt.Errorf("os.MkdirAll: %w", err)
	}

	x.config.Debugf("Extracting File: %v to %v", filename, resp.Output)

	bytes, files, archives, err := ExtractFile(&XFile{ // extract the file.
		FilePath:  filename,
		OutputDir: resp.Output,
		FileMode:  x.config.FileMode,
		DirMode:   x.config.DirMode,
		Passwords: resp.X.Passwords,
		Password:  resp.X.Password,
	})

	if err != nil {
		x.DeleteFiles(resp.Output) // clean up the mess after an error and bail.
	}

	return bytes, files, archives, err
}

func (x *Xtractr) cleanupProcessedArchives(resp *Response) error {
	if resp.X.LogFile {
		x.createLogFile(resp)
	}

	if resp.X.DeleteOrig {
		// as requested
		x.deleteOriginals(resp)
	}

	var err error

	if !resp.X.TempFolder {
		// If TempFolder is false then move the files back to the original location.
		resp.NewFiles, err = x.MoveFiles(resp.Output, resp.X.SearchPath, false)
	}

	if err != nil {
		return err
	}

	files, err := x.GetFileList(resp.X.SearchPath)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		// If the original path is empty, delete it.
		x.DeleteFiles(resp.X.SearchPath)
	}

	return nil
}

func (x *Xtractr) createLogFile(resp *Response) {
	tmpFile := filepath.Join(resp.Output, x.config.Suffix+"."+filepath.Base(resp.X.SearchPath)+".txt")
	resp.NewFiles = append(resp.NewFiles, tmpFile)

	msg := []byte(fmt.Sprintf("# %s - this file may be removed with the extracted data\n---\n"+
		"archives:%s\nextras:%v\nfrom_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n",
		x.config.Suffix, resp.Archives, resp.Extras, resp.X.SearchPath, resp.Output, !resp.X.TempFolder, time.Now(),
		strings.Join(resp.NewFiles, "\n  - ")))

	if err := os.WriteFile(tmpFile, msg, x.config.FileMode); err != nil {
		x.config.Printf("Error: Creating Temporary Tracking File: %v", err)
	}
}

func (x *Xtractr) deleteOriginals(resp *Response) {
	for _, archives := range resp.Archives {
		x.DeleteFiles(archives...)
	}
	// these got extracted too
	for _, archives := range resp.Extras {
		if len(archives) != 0 {
			x.DeleteFiles(archives...)
		}
	}
}

func (x *Xtractr) cleanTempFolder(resp *Response) {
	noSuffix := strings.TrimSuffix(strings.TrimRight(resp.Output, `/\`), x.config.Suffix)
	if _, err := os.Stat(noSuffix); err == nil {
		return // it exists already?!
	} else if _, err := os.Stat(resp.Output); err != nil {
		return
	}

	if newFiles, err := x.MoveFiles(resp.Output, noSuffix, false); err != nil {
		x.config.Printf("Error: Renaming Temporary Folder: %v", err)
	} else {
		x.config.Debugf("Renamed Temp Folder: %v -> %v", resp.Output, noSuffix)
		resp.Output = noSuffix
		resp.NewFiles = newFiles
	}

	files, err := x.GetFileList(resp.X.SearchPath)
	if err != nil {
		x.config.Printf("Error: Reading SearchPath: %v", err)
		return
	}

	if len(files) == 0 {
		// If the original path is empty, delete it.
		x.DeleteFiles(resp.X.SearchPath)
	}
}
