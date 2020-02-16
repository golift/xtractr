package extractorr

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

// Extract defines the data needed to extract a path.
type Extract struct {
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
func (e *Extractorr) Extract(ex *Extract) (int, error) {
	if e.queue == nil {
		return -1, fmt.Errorf("extractorr is stopped, cannot queue")
	}

	e.queue <- ex // goes to processQueue()

	return len(e.queue), nil
}

// processQueue runs in a go routine, 'e.Parallel' times,
// and watches for things to extract.
func (e *Extractorr) processQueue() {
	for ex := range e.queue {
		e.extract(ex)
	}
}

// extract is where the real work begins and files get extracted.
// This is fired off from the queue.
func (e *Extractorr) extract(ex *Extract) {
	re := &Response{
		Name:    ex.Name,
		Started: time.Now(),
		Output:  filepath.Join(ex.SearchPath, e.Config.Suffix),
	}

	re.Archives = FindCompressedFiles(ex.SearchPath)

	if len(re.Archives) < 1 {
		e.finishExtract(ex, re, fmt.Errorf("no compressed files found"))
		return
	}

	if ex.CBFunction != nil {
		re.Queued = len(e.queue)
		ex.CBFunction(re) // This lets the calling function know we've started.
	}

	// e.log("Starting: %d archives - %v", len(resp.Archives), ex.SearchPath)
	e.finishExtract(ex, re, e.decompressFiles(ex, re))
}

func (e *Extractorr) finishExtract(ex *Extract, re *Response, err error) {
	if ex.CBFunction == nil {
		// log something.
		return
	}

	re.Error = err
	re.Elapsed = time.Since(re.Started)
	re.Done = true
	re.Queued = len(e.queue)
	ex.CBFunction(re) // This lets the calling function know we've finished.
}

// decompressFiles runs after we find and verify archives exist.
func (e *Extractorr) decompressFiles(ex *Extract, re *Response) error {
	err := os.MkdirAll(re.Output, 0755)
	if err != nil {
		return err
	}

	re.Extras, re.Size, re.AllFiles, err = e.processArchives(re.Output, re.Archives)
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(re.Output, e.Config.Suffix)
	msg := fmt.Sprintf("%s - this file is removed with the extracted data\n"+
		"from: %s\npath: %s\ntime: %v\nfiles:\n%v\n%v\n", e.Config.Suffix,
		ex.SearchPath, re.Output, time.Now(), tmpFile, strings.Join(re.NewFiles, "\n"))

	if err := ioutil.WriteFile(tmpFile, []byte(msg), 0744); err != nil {
		e.log("Error: Creating Temporary Tracking File: %v", err)
	}

	// Add the file we just wrote to the list of files written.
	re.NewFiles = append(e.GetFileList(re.Output), tmpFile)

	if ex.DeleteOrig {
		e.DeleteFiles(re.Archives...) // as requested
	}

	if !ex.TempFolder {
		// Move the extracted files back into their original folder.
		re.NewFiles, err = e.MoveFiles(re.Output, ex.SearchPath)
		if err != nil {
			if !ex.DeleteOrig {
				// cleanup the broken decompression, but only if we didn't delete the originals.
				e.DeleteFiles(re.Output)
			}

			return err
		}
	}

	return nil
}

// Extract one archive at a time, then check if it contained any more archives.
// Returns list of extra files extracted, size of data written, files written.
func (e *Extractorr) processArchives(tmpPath string, archives []string) ([]string, int64, []string, error) {
	files, extras := []string{}, []string{}
	size := int64(0)

	for _, filename := range archives {
		e.debug("Extracting File: %v", filename)
		beforeFiles := e.GetFileList(tmpPath) // get the "before this extraction" file list
		ss, ff, err := ExtractFile(filename, tmpPath)
		files = append(files, ff...)
		size += ss

		if err != nil {
			e.DeleteFiles(tmpPath)
			return extras, size, files, err
		}

		newFiles := Difference(beforeFiles, e.GetFileList(tmpPath))

		// Check if we just extracted more archives.
		for _, filename := range newFiles {
			if strings.HasSuffix(filename, ".rar") || strings.HasSuffix(filename, ".zip") {
				// recurse and append data to tracking vars.
				ee, ss, ff, err := e.processArchives(tmpPath, []string{filename})
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
