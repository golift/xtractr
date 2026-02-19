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
	// Folder path and filters describing where and how to find archives.
	Filter

	// Unused in this app; exposed for calling library.
	Name string
	// Archive password. Only supported with RAR and 7zip files. Prepended to Passwords.
	Password string
	// Archive passwords (try multiple). Only supported with RAR and 7zip files.
	Passwords []string
	// Set DisableRecursion to true if you want to avoid extracting archives inside archives.
	DisableRecursion bool
	// Set RecurseISO to true if you want to recursively extract archives in ISO files.
	// If ISOs and other archives are found, none will not extract recursively if this is false.
	RecurseISO bool
	// Folder to extract data. Default is same level as SearchPath with a suffix.
	ExtractTo string
	// Leave files in temporary folder? false=move files back to Filter.Path
	// Moving files back will cause the "extracted files" returned to only contain top-level items.
	TempFolder bool
	// Delete Archives after successful extraction? Be careful.
	DeleteOrig bool
	// Create a log (.txt) file of the extraction information.
	LogFile bool
	// Callback Function, runs twice per queued item.
	CBFunction func(*Response)
	// Callback Channel, msg sent twice per queued item.
	CBChannel chan *Response
	// Progress is called periodically during file extraction.
	// Contains info about the progress of the extraction.
	// This is not called if an Updates channel is also provided.
	// Shared by all archive file extractions that occur with this Xtract.
	Progress func(Progress)
	// If an Updates channel is provided, all Progress updates are sent to it.
	// Contains info about the progress of the extraction.
	// Shared by all archive file extractions that occur with this Xtract.
	Updates chan Progress
}

// Response is sent to the call-back function. The first CBFunction call is just
// a notification that the extraction has started. You can determine it's the first
// call by checking Response.Done. false = started, true = finished. When done=false
// the only other meaningful data provided is the re.Archives, re.Output and re.Queue.
type Response struct {
	// Extract Started (false) or Finished (true).
	Done bool
	// Size of data written.
	Size uint64
	// Temporary output folder.
	Output string
	// Items still in queue.
	Queued int
	// When this extract began.
	Started time.Time
	// Elapsed extraction duration. ie. How long it took.
	Elapsed time.Duration
	// Extra archives extracted from within an archive.
	Extras ArchiveList
	// Initial archives found and extracted.
	Archives ArchiveList
	// Files written to final path.
	NewFiles []string
	// Error encountered, only when done=true.
	Error error
	// Copied from input data.
	X *Xtract
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

const fsSyncDelay = 10 * time.Second

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
		Output:   strings.TrimRight(ext.Path, `/\`) + x.config.Suffix, // tmp folder.
		Archives: FindCompressedFiles(ext.Filter),
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
		Archives: make(ArchiveList),
		Extras:   make(ArchiveList),
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
	allArchives := make(ArchiveList)

	for subDir := range resp.Archives {
		output := resp.Output
		if resp.X.TempFolder {
			// If we keep the temp folder, then use elaborate extraction paths.
			output = filepath.Join(resp.Output, strings.TrimPrefix(subDir, resp.X.Path))
		}

		subResp := &Response{
			X: &Xtract{
				Filter: Filter{
					Path:          subDir,
					ExcludeSuffix: resp.X.ExcludeSuffix,
				},
				Name:             resp.X.Name,
				Password:         resp.X.Password,
				Passwords:        resp.X.Passwords,
				DisableRecursion: resp.X.DisableRecursion,
				RecurseISO:       resp.X.RecurseISO,
				ExtractTo:        resp.X.ExtractTo,
				DeleteOrig:       resp.X.DeleteOrig,
				TempFolder:       resp.X.TempFolder,
				LogFile:          resp.X.LogFile,
				Updates:          resp.X.Updates,
				Progress:         resp.X.Progress,
			},
			Started:  resp.Started,
			Output:   output,
			Archives: ArchiveList{subDir: resp.Archives[subDir]},
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
		x.config.Printf("Error Extracting: %s (%v elapsed): %v", resp.X.Path, resp.Elapsed, err)
		return
	}

	x.config.Printf("Finished Extracting: %s (%v elapsed, queue size: %d)", resp.X.Path, resp.Elapsed, resp.Queued)
}

// weExtractedAnISO makes sure we do not recurse into an ISO file.
// If an iso was found in the same directory as other archives,
// it will prevent the other archives from extracting recursively.
func weExtractedAnISO(resp *Response) bool {
	for _, archives := range resp.Archives {
		for _, archive := range archives {
			if strings.HasSuffix(strings.ToLower(archive), ".iso") {
				return true
			}
		}
	}

	return false
}

// decompressFiles runs after we find and verify archives exist.
// This extracts everything in the search path then (optionally)
// checks the output path for more archives that were just decompressed.
func (x *Xtractr) decompressFiles(resp *Response) error {
	err := x.decompressArchives(resp)
	if err != nil {
		return err
	}

	if resp.X.DisableRecursion || (!resp.X.RecurseISO && weExtractedAnISO(resp)) {
		return x.cleanupProcessedArchives(resp)
	}

	// Now do it again with the output folder.
	resp.Extras = FindCompressedFiles(Filter{
		Path:          resp.Output,
		ExcludeSuffix: resp.X.ExcludeSuffix,
	})
	nre := &Response{
		X: &Xtract{
			Password:  resp.X.Password,
			Passwords: resp.X.Passwords,
			Progress:  resp.X.Progress,
			Updates:   resp.X.Updates,
		},
		Started:  resp.Started,
		Output:   resp.Output,
		Archives: resp.Extras,
	}
	err = x.decompressArchives(nre)
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
func (x *Xtractr) processArchive(filename string, resp *Response) (uint64, []string, []string, error) {
	err := os.MkdirAll(resp.Output, x.config.DirMode)
	if err != nil {
		return 0, nil, nil, NewExtractError(
			fmt.Errorf("making output dir: %w", err),
			filename, resp.Output, 0, "directory",
		)
	}

	x.config.Debugf("Extracting File: %v to %v", filename, resp.Output)

	xFile := &XFile{
		FilePath:    filename,
		OutputDir:   resp.Output,
		FileMode:    x.config.FileMode,
		DirMode:     x.config.DirMode,
		Passwords:   resp.X.Passwords,
		Password:    resp.X.Password,
		FileWorkers: x.config.FileWorkers,
		log:         x.config.Logger,
		Updates:     resp.X.Updates,
		Progress:    resp.X.Progress,
	}
	bytes, files, archives, err := ExtractFile(xFile)
	if err != nil {
		x.DeleteFiles(resp.Output) // clean up the mess after an error and bail.
		return bytes, files, archives, WrapExtractError(err, xFile, bytes, "")
	}

	return bytes, files, archives, nil
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
		time.Sleep(fsSyncDelay) // Wait for file system to catch up/sync.
		// If TempFolder is false then move the files back to the original location.
		resp.NewFiles, err = x.MoveFiles(resp.Output, resp.X.Path, false)
	}

	if err != nil {
		return err
	}

	files, err := x.GetFileList(resp.X.Path)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		// If the original path is empty, delete it.
		x.DeleteFiles(resp.X.Path)
	}

	return nil
}

func (x *Xtractr) createLogFile(resp *Response) {
	tmpFile := filepath.Join(resp.Output, x.config.Suffix+"."+filepath.Base(resp.X.Path)+".txt")
	resp.NewFiles = append(resp.NewFiles, tmpFile)

	msg := fmt.Appendf(nil, "# %s - this file may be removed with the extracted data\n---\n"+
		"archives:%s\nextras:%v\nfrom_path:%s\ntemp_path:%s\nrelocated:%v\ntime:%v\nfiles:\n  - %v\n",
		x.config.Suffix, resp.Archives, resp.Extras, resp.X.Path, resp.Output, !resp.X.TempFolder, time.Now(),
		strings.Join(resp.NewFiles, "\n  - "))

	err := os.WriteFile(tmpFile, msg, x.config.FileMode)
	if err != nil {
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

// getTempFolderFinalName returns the name of the final output folder when stored in the temp folder.
func (x *Xtractr) getTempFolderFinalName(resp *Response) string {
	// If the name is taken, try up to 999 different names.
	const tryNames = 999

	_, err := os.Stat(resp.Output)
	if err != nil {
		return "" // the output folder was deleted?!
	}

	newName := strings.TrimSuffix(strings.TrimRight(resp.Output, `/\`), x.config.Suffix)
	// If the original thing we extracted was an archive (not a dir), remove the suffix from the output folder.
	if IsArchiveFile(resp.X.Name) {
		newName = strings.TrimSuffix(newName, filepath.Ext(newName))
	}

	if IsArchiveFile(newName) { // We do it twice in case of `tar.gz` etc.
		newName = strings.TrimSuffix(newName, filepath.Ext(newName))
	}

	_, err = os.Stat(newName)
	if x.config.TryNames && err == nil {
		for i := range tryNames {
			loopName := newName + fmt.Sprint(".", i)

			_, err = os.Stat(loopName)
			if err != nil {
				return loopName
			}
		}
	}

	_, err = os.Stat(newName)
	if err == nil {
		return "" // it exists already?!
	}

	return newName
}

func (x *Xtractr) cleanTempFolder(resp *Response) {
	newName := x.getTempFolderFinalName(resp)
	if newName == "" {
		return
	}

	newFiles, err := x.MoveFiles(resp.Output, newName, false)
	if err != nil {
		x.config.Printf("Error: Renaming Temporary Folder: %v", err)
	} else {
		x.config.Debugf("Renamed Temp Folder: %v -> %v", resp.Output, newName)
		resp.Output = newName
		resp.NewFiles = newFiles
	}

	files, err := x.GetFileList(resp.X.Path)
	if err != nil {
		x.config.Printf("Error: Reading SearchPath: %v", err)
		return
	}

	if len(files) == 0 {
		// If the original path is empty, delete it.
		x.DeleteFiles(resp.X.Path)
	}
}
