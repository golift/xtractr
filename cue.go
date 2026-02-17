package xtractr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// CUE sheet parsing errors.
var (
	ErrNoCueFile        = errors.New("cue sheet does not reference a FILE")
	ErrNoTracks         = errors.New("cue sheet contains no tracks")
	ErrAudioNotFound    = errors.New("audio file referenced by cue sheet not found")
	ErrUnsupportedAudio = errors.New("cue sheet references unsupported audio format (only FLAC is supported)")
)

// CueSheet represents a parsed CUE sheet.
type CueSheet struct {
	// Performer is the album-level performer.
	Performer string
	// Title is the album title.
	Title string
	// File is the referenced audio file name (e.g. "album.flac").
	File string
	// FileType is the file type from the CUE sheet (e.g. "WAVE", "BINARY").
	FileType string
	// Tracks contains the list of tracks in order.
	Tracks []CueTrack
}

// CueTrack represents a single track in a CUE sheet.
type CueTrack struct {
	// Number is the track number (1-based).
	Number int
	// Title is the track title.
	Title string
	// Performer is the track-level performer (falls back to album performer).
	Performer string
	// StartSample is the starting sample position for this track.
	StartSample uint64
}

// cueTimestamp holds the raw parsed CUE time (MM:SS:FF).
type cueTimestamp struct {
	minutes int
	seconds int
	frames  int // CD frames, 75 per second
}

// cdFramesPerSecond is the number of frames per second in CD audio (75 fps).
const cdFramesPerSecond = 75

// toSamples converts a CUE timestamp to a sample position at the given sample rate.
func (t cueTimestamp) toSamples(sampleRate uint32) uint64 {
	const secondsPerMinute = 60

	totalSeconds := uint64(t.minutes)*secondsPerMinute + uint64(t.seconds)
	samples := totalSeconds * uint64(sampleRate)
	// Add fractional second from CD frames.
	samples += uint64(t.frames) * uint64(sampleRate) / cdFramesPerSecond

	return samples
}

// ExtractCUE extracts individual tracks from a FLAC file referenced by a CUE sheet.
// The xFile.FilePath should point to the .cue file.
func ExtractCUE(xFile *XFile) (size uint64, files, archives []string, err error) {
	cue, timestamps, err := parseCueSheetFile(xFile.FilePath)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("parsing cue sheet: %w", err)
	}

	// Resolve the audio file path relative to the CUE file.
	cueDir := filepath.Dir(xFile.FilePath)
	audioPath := filepath.Join(cueDir, cue.File)

	// Check that the audio file exists.
	_, err = os.Stat(audioPath)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %s", ErrAudioNotFound, audioPath)
	}

	// Only FLAC is supported for now.
	ext := strings.ToLower(filepath.Ext(cue.File))
	if ext != ".flac" {
		return 0, nil, nil, fmt.Errorf("%w: %s", ErrUnsupportedAudio, ext)
	}

	size, files, err = splitFLAC(xFile, audioPath, cue, timestamps)
	if err != nil {
		return 0, nil, nil, err
	}

	// The archive list includes both the CUE file and the FLAC file.
	archives = []string{xFile.FilePath, audioPath}

	return size, files, archives, nil
}

// parseCueSheetFile parses a CUE sheet from a file path and returns the sheet plus raw timestamps.
func parseCueSheetFile(path string) (*CueSheet, []cueTimestamp, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening cue sheet: %w", err)
	}
	defer file.Close()

	return parseCueSheet(file)
}

// parseCueSheet parses a CUE sheet from an io.Reader.
func parseCueSheet(reader io.Reader) (*CueSheet, []cueTimestamp, error) { //nolint:gocognit,cyclop,funlen
	cue := &CueSheet{}
	scanner := bufio.NewScanner(reader)
	timestamps := []cueTimestamp{}

	var (
		currentTrack     *CueTrack
		currentTimestamp cueTimestamp
		hasTimestamp     bool
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "REM ") {
			continue
		}

		cmd, args := splitCueLine(line)

		switch cmd {
		case "PERFORMER":
			performer := unquoteCue(args)
			if currentTrack != nil {
				currentTrack.Performer = performer
			} else {
				cue.Performer = performer
			}
		case "TITLE":
			title := unquoteCue(args)
			if currentTrack != nil {
				currentTrack.Title = title
			} else {
				cue.Title = title
			}
		case "FILE":
			fileName, fileType := parseCueFileCmd(args)
			cue.File = fileName
			cue.FileType = fileType
		case "TRACK":
			if currentTrack != nil {
				cue.Tracks = append(cue.Tracks, *currentTrack)

				if hasTimestamp {
					timestamps = append(timestamps, currentTimestamp)
				} else {
					timestamps = append(timestamps, cueTimestamp{})
				}
			}

			trackNum := parseCueTrackNum(args)
			currentTrack = &CueTrack{Number: trackNum}
			hasTimestamp = false
			currentTimestamp = cueTimestamp{}
		case "INDEX":
			if currentTrack != nil {
				indexNum, timestamp := parseCueIndex(args)
				if indexNum == 1 {
					currentTimestamp = timestamp
					hasTimestamp = true
				}
			}
		}
	}

	// Save the last track.
	if currentTrack != nil {
		cue.Tracks = append(cue.Tracks, *currentTrack)

		if hasTimestamp {
			timestamps = append(timestamps, currentTimestamp)
		} else {
			timestamps = append(timestamps, cueTimestamp{})
		}
	}

	err := scanner.Err()
	if err != nil {
		return nil, nil, fmt.Errorf("reading cue sheet: %w", err)
	}

	if cue.File == "" {
		return nil, nil, ErrNoCueFile
	}

	if len(cue.Tracks) == 0 {
		return nil, nil, ErrNoTracks
	}

	// Fill in album-level performer for tracks that don't specify one.
	for idx := range cue.Tracks {
		if cue.Tracks[idx].Performer == "" {
			cue.Tracks[idx].Performer = cue.Performer
		}
	}

	return cue, timestamps, nil
}

// splitCueLine splits a CUE line into its command and arguments.
func splitCueLine(line string) (string, string) {
	parts := strings.SplitN(line, " ", 2) //nolint:mnd
	if len(parts) < 2 {                   //nolint:mnd
		return strings.ToUpper(parts[0]), ""
	}

	return strings.ToUpper(parts[0]), parts[1]
}

// unquoteCue removes surrounding double quotes from a CUE sheet value.
func unquoteCue(val string) string {
	val = strings.TrimSpace(val)
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		return val[1 : len(val)-1]
	}

	return val
}

// parseCueFileCmd parses the FILE command arguments: "filename.flac" WAVE.
func parseCueFileCmd(args string) (string, string) {
	var fileName, fileType string

	if args != "" && args[0] == '"' {
		// Find closing quote.
		end := strings.Index(args[1:], "\"")
		if end >= 0 {
			fileName = args[1 : end+1]
			fileType = strings.TrimSpace(args[end+2:])
		} else {
			fileName = unquoteCue(args)
		}
	} else {
		parts := strings.SplitN(args, " ", 2) //nolint:mnd
		fileName = parts[0]

		if len(parts) > 1 {
			fileType = strings.TrimSpace(parts[1])
		}
	}

	return fileName, fileType
}

// parseCueTrackNum parses the track number from TRACK args like "01 AUDIO".
func parseCueTrackNum(args string) int {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return 0
	}

	num, _ := strconv.Atoi(parts[0])

	return num
}

// parseCueIndex parses the INDEX command args like "01 03:45:12".
func parseCueIndex(args string) (int, cueTimestamp) {
	parts := strings.Fields(args)
	if len(parts) < 2 { //nolint:mnd
		return 0, cueTimestamp{}
	}

	indexNum, _ := strconv.Atoi(parts[0])
	timestamp := parseCueTime(parts[1])

	return indexNum, timestamp
}

// cueTimeRegex matches the MM:SS:FF timestamp format.
var cueTimeRegex = regexp.MustCompile(`^(\d+):(\d+):(\d+)$`)

// parseCueTime parses a CUE timestamp string in MM:SS:FF format.
func parseCueTime(s string) cueTimestamp {
	matches := cueTimeRegex.FindStringSubmatch(s)
	if matches == nil {
		return cueTimestamp{}
	}

	minutes, _ := strconv.Atoi(matches[1])
	seconds, _ := strconv.Atoi(matches[2])
	frames, _ := strconv.Atoi(matches[3])

	return cueTimestamp{
		minutes: minutes,
		seconds: seconds,
		frames:  frames,
	}
}

// splitFLAC splits a FLAC file into individual tracks based on CUE sheet data.
func splitFLAC(xFile *XFile, audioPath string, cue *CueSheet, timestamps []cueTimestamp) (uint64, []string, error) {
	// Open, parse, and read all frames from the FLAC file.
	// We close the stream immediately after reading to release the file handle,
	// which is important on Windows where open handles block TempDir cleanup.
	streamInfo, allFrames, err := readFLACFile(audioPath)
	if err != nil {
		return 0, nil, err
	}

	sampleRate := streamInfo.SampleRate
	totalSamples := streamInfo.NSamples

	// Convert CUE timestamps to sample positions.
	trackStarts := make([]uint64, len(cue.Tracks))
	for idx, ts := range timestamps {
		trackStarts[idx] = ts.toSamples(sampleRate)
	}

	// Calculate track end samples (each track ends where the next begins).
	trackEnds := make([]uint64, len(cue.Tracks))
	for idx := range cue.Tracks {
		if idx < len(cue.Tracks)-1 {
			trackEnds[idx] = trackStarts[idx+1]
		} else {
			trackEnds[idx] = totalSamples
		}
	}

	// Ensure output directory exists.
	err = os.MkdirAll(xFile.OutputDir, xFile.DirMode)
	if err != nil {
		return 0, nil, fmt.Errorf("creating output directory: %w", err)
	}

	defer xFile.newProgress(0, 0, len(cue.Tracks)).done()

	var (
		totalSize uint64
		files     []string
	)

	// Split frames into tracks.
	for trackIdx := range cue.Tracks {
		track := &cue.Tracks[trackIdx]
		startSample := trackStarts[trackIdx]
		endSample := trackEnds[trackIdx]

		if endSample <= startSample {
			continue
		}

		outputName := formatTrackFilename(track)
		outputPath := filepath.Join(xFile.OutputDir, outputName)

		size, err := writeTrackFLAC(outputPath, streamInfo, allFrames, startSample, endSample, xFile.FileMode)
		if err != nil {
			return totalSize, files, fmt.Errorf("writing track %d: %w", track.Number, err)
		}

		totalSize += size

		files = append(files, outputPath)
		xFile.Debugf("Wrote track %d: %s (%d bytes)", track.Number, outputPath, size)
	}

	return totalSize, files, nil
}

// flacFrame holds a parsed frame along with its sample position.
type flacFrame struct {
	frame       *frame.Frame
	sampleStart uint64
	sampleEnd   uint64
}

// readFLACFile opens a FLAC file, reads all frames, and closes the file.
// We open and close the os.File ourselves because flac.Open wraps the reader
// in bufio.NewReader, which loses the io.Closer interface and prevents
// flac.Stream.Close from actually closing the underlying file handle.
// This matters on Windows where open handles block file deletion.
func readFLACFile(audioPath string) (*meta.StreamInfo, []flacFrame, error) {
	file, err := os.Open(audioPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening flac file: %w", err)
	}

	stream, err := flac.New(file)
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("parsing flac file: %w", err)
	}

	info := stream.Info
	frames, err := readAllFrames(stream)

	// Always close the file handle, regardless of readAllFrames result.
	_ = file.Close()

	if err != nil {
		return nil, nil, err
	}

	return info, frames, nil
}

// readAllFrames reads all audio frames from a FLAC stream.
func readAllFrames(stream *flac.Stream) ([]flacFrame, error) {
	var (
		frames    []flacFrame
		samplePos uint64
	)

	for {
		parsed, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("parsing flac frame: %w", err)
		}

		nsamples := uint64(parsed.Subframes[0].NSamples)
		frames = append(frames, flacFrame{
			frame:       parsed,
			sampleStart: samplePos,
			sampleEnd:   samplePos + nsamples,
		})
		samplePos += nsamples
	}

	return frames, nil
}

// writeTrackFLAC writes a subset of FLAC frames to a new FLAC file for a single track.
// Frames are split at sample boundaries when they span track boundaries.
func writeTrackFLAC( //nolint:funlen
	outputPath string,
	srcInfo *meta.StreamInfo,
	allFrames []flacFrame,
	startSample, endSample uint64,
	fileMode os.FileMode,
) (uint64, error) {
	outFile, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return 0, fmt.Errorf("creating output flac file: %w", err)
	}

	trackSamples := endSample - startSample

	// Create a new StreamInfo for this track.
	trackInfo := &meta.StreamInfo{
		BlockSizeMin:  srcInfo.BlockSizeMin,
		BlockSizeMax:  srcInfo.BlockSizeMax,
		FrameSizeMin:  srcInfo.FrameSizeMin,
		FrameSizeMax:  srcInfo.FrameSizeMax,
		SampleRate:    srcInfo.SampleRate,
		NChannels:     srcInfo.NChannels,
		BitsPerSample: srcInfo.BitsPerSample,
		NSamples:      trackSamples,
	}

	enc, err := flac.NewEncoder(outFile, trackInfo)
	if err != nil {
		_ = outFile.Close()
		return 0, fmt.Errorf("creating flac encoder: %w", err)
	}

	for idx := range allFrames {
		srcFrame := &allFrames[idx]
		// Skip frames entirely outside the track range.
		if srcFrame.sampleEnd <= startSample || srcFrame.sampleStart >= endSample {
			continue
		}

		// Determine which portion of this frame belongs to the track.
		clipStart := max(srcFrame.sampleStart, startSample)
		clipEnd := min(srcFrame.sampleEnd, endSample)

		origSamples := int(srcFrame.sampleEnd - srcFrame.sampleStart)
		offsetInFrame := int(clipStart - srcFrame.sampleStart)
		samplesToTake := int(clipEnd - clipStart)

		if samplesToTake <= 0 {
			continue
		}

		outFrame := buildOutputFrame(srcFrame.frame, offsetInFrame, samplesToTake, origSamples)

		err = enc.WriteFrame(outFrame)
		if err != nil {
			_ = outFile.Close()
			return 0, fmt.Errorf("writing flac frame: %w", err)
		}
	}

	// enc.Close() also closes the underlying file via io.Closer.
	err = enc.Close()
	if err != nil {
		return 0, fmt.Errorf("closing flac encoder: %w", err)
	}

	// Stat the file after closing to get the final size.
	stat, err := os.Stat(outputPath)
	if err != nil {
		return 0, fmt.Errorf("stat output file: %w", err)
	}

	return uint64(stat.Size()), nil
}

// buildOutputFrame creates a new frame with a subset of samples from the source frame.
func buildOutputFrame(src *frame.Frame, offset, count, origSamples int) *frame.Frame {
	// If the frame is entirely within the track, just return it as-is.
	if offset == 0 && count == origSamples {
		return src
	}

	// We need to slice the samples. First correlate to get proper L/R samples.
	src.Correlate()

	outFrame := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: false,
			BlockSize:         uint16(count),
			SampleRate:        src.SampleRate,
			Channels:          src.Channels,
			BitsPerSample:     src.BitsPerSample,
		},
	}

	outFrame.Subframes = make([]*frame.Subframe, len(src.Subframes))

	for ch, sub := range src.Subframes {
		newSamples := make([]int32, count)
		copy(newSamples, sub.Samples[offset:offset+count])

		outFrame.Subframes[ch] = &frame.Subframe{
			SubHeader: frame.SubHeader{
				Pred:  frame.PredVerbatim,
				Order: 0,
			},
			Samples:  newSamples,
			NSamples: count,
		}
	}

	return outFrame
}

// formatTrackFilename generates a filename for an extracted track.
func formatTrackFilename(track *CueTrack) string {
	title := track.Title
	if title == "" {
		title = fmt.Sprintf("Track %d", track.Number)
	}

	title = sanitizeFilename(title)

	return fmt.Sprintf("%02d - %s.flac", track.Number, title)
}

// sanitizeFilename removes or replaces characters that are problematic in filenames.
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
	)

	return replacer.Replace(name)
}
