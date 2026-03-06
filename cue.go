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
	// Some CUE sheets say FILE "album.wav" WAVE but the file on disk is album.flac; try .flac when .wav is missing.
	cueDir := filepath.Dir(xFile.FilePath)

	audioPath, err := resolveCueAudioPath(cueDir, cue.File)
	if err != nil {
		return 0, nil, nil, err
	}

	// Only FLAC is supported for now.
	ext := strings.ToLower(filepath.Ext(audioPath))
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

// resolveCueAudioPath returns the path to the audio file referenced by the CUE.
// If the CUE says FILE "album.wav" but the file on disk is album.flac, the .flac path is returned.
func resolveCueAudioPath(cueDir, cueFile string) (string, error) {
	path := filepath.Join(cueDir, cueFile)

	_, err := os.Stat(path)
	if err == nil {
		return path, nil
	}

	ext := strings.ToLower(filepath.Ext(cueFile))
	if ext == ".wav" {
		flacPath := path[:len(path)-len(ext)] + ".flac"

		_, err = os.Stat(flacPath)
		if err == nil {
			return flacPath, nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrAudioNotFound, path)
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
	// Open, parse, and read all frames and metadata (e.g. front cover) from the FLAC file.
	// We close the stream immediately after reading to release the file handle,
	// which is important on Windows where open handles block TempDir cleanup.
	streamInfo, allFrames, coverPicture, err := readFLACFile(audioPath)
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

	var (
		coverPath string
		writeErr  error
	)

	if coverPicture != nil {
		coverPath, writeErr = writeCoverToFile(xFile.OutputDir, coverPicture, xFile.FileMode)
		if writeErr != nil {
			xFile.Debugf("Error writing album cover: %s", writeErr)
		}

		xFile.Debugf("Wrote album cover: %s (%d bytes)", coverPath, len(coverPicture.Data))
	}

	defer xFile.newProgress(0, 0, len(cue.Tracks)).done()

	totalSize, files, err := writeTracksFLAC(xFile, cue, allFrames, trackStarts, streamInfo, trackEnds, coverPicture)
	if err != nil {
		return 0, nil, err
	}

	if coverPath != "" {
		return totalSize + uint64(len(coverPicture.Data)), append(files, coverPath), nil
	}

	return totalSize, files, nil
}

func writeTracksFLAC(
	xFile *XFile,
	cue *CueSheet,
	allFrames []flacFrame,
	trackStarts []uint64,
	streamInfo *meta.StreamInfo,
	trackEnds []uint64,
	coverPicture *meta.Picture,
) (uint64, []string, error) {
	var (
		totalSize uint64
		files     = make([]string, 0, len(cue.Tracks))
	)

	// Split frames into tracks.
	for trackIdx := range cue.Tracks {
		startSample := trackStarts[trackIdx]

		endSample := trackEnds[trackIdx]
		if endSample <= startSample {
			continue
		}

		track := &cue.Tracks[trackIdx]
		outputName := formatTrackFilename(track)
		outputPath := filepath.Join(xFile.OutputDir, outputName)
		blocks := buildTrackMetadataBlocks(cue, track, coverPicture)

		size, err := writeTrackFLAC(outputPath, streamInfo, allFrames, startSample, endSample, xFile.FileMode, blocks)
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

// frontCoverPictureType is the FLAC/ID3v2 APIC picture type for "Cover (front)".
const frontCoverPictureType = 3

// readFLACFile opens a FLAC file, parses metadata and all frames, and closes the file.
// It returns the stream info, frames, and the first front-cover picture block if present.
// We use flac.Parse (not flac.New) so metadata blocks (e.g. Picture) are available.
// We open and close the os.File ourselves so the underlying file handle is released
// (important on Windows where open handles block TempDir cleanup).
func readFLACFile(audioPath string) (*meta.StreamInfo, []flacFrame, *meta.Picture, error) {
	file, err := os.Open(audioPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("opening flac file: %w", err)
	}
	defer file.Close()

	stream, err := flac.Parse(file)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parsing flac file: %w", err)
	}

	frames, err := readAllFrames(stream)
	if err != nil {
		return nil, nil, nil, err
	}

	var cover *meta.Picture

	for _, blk := range stream.Blocks {
		if blk.Type != meta.TypePicture {
			continue
		}

		pic, ok := blk.Body.(*meta.Picture)
		if !ok || pic.Type != frontCoverPictureType {
			continue
		}

		cover = pic

		break
	}

	return stream.Info, frames, cover, nil
}

// buildVorbisCommentBlock returns a FLAC metadata block with ALBUM, ARTIST, TITLE, and TRACKNUMBER
// from the CUE sheet and track so that split files can be identified by Lidarr and other importers.
func buildVorbisCommentBlock(cue *CueSheet, track *CueTrack) *meta.Block {
	artist := track.Performer
	if artist == "" {
		artist = cue.Performer
	}

	title := track.Title
	if title == "" {
		title = fmt.Sprintf("Track %d", track.Number)
	}

	tags := [][2]string{
		{"TITLE", title},
		{"TRACKNUMBER", strconv.Itoa(track.Number)},
	}

	if cue.Title != "" {
		tags = append(tags, [2]string{"ALBUM", cue.Title})
	}

	if artist != "" {
		tags = append(tags, [2]string{"ARTIST", artist})
	}

	comment := &meta.VorbisComment{
		Vendor: "golift.io/xtractr",
		Tags:   tags,
	}

	return &meta.Block{
		Header: meta.Header{Type: meta.TypeVorbisComment, Length: 1},
		Body:   comment,
	}
}

// buildTrackMetadataBlocks returns metadata blocks for a split track: VorbisComment
// (when cue/track are set) and optionally the front-cover Picture. The last block
// has IsLast set so the FLAC encoder writes the metadata block chain correctly.
func buildTrackMetadataBlocks(cue *CueSheet, track *CueTrack, coverPicture *meta.Picture) []*meta.Block {
	var blocks []*meta.Block

	if cue != nil && track != nil {
		blocks = append(blocks, buildVorbisCommentBlock(cue, track))
	}

	if coverPicture != nil {
		blocks = append(blocks, &meta.Block{
			Header: meta.Header{Type: meta.TypePicture, Length: 1, IsLast: false},
			Body:   coverPicture,
		})
	}

	if len(blocks) > 0 {
		blocks[len(blocks)-1].IsLast = true
	}

	return blocks
}

// writeCoverToFile writes the front-cover picture data to a file in outputDir.
// The filename is chosen from the picture's MIME type: cover.png for image/png,
// cover.jpg for image/jpeg, otherwise cover.bin.
func writeCoverToFile(outputDir string, pic *meta.Picture, fileMode os.FileMode) (string, error) {
	ext := "bin"

	switch {
	case strings.EqualFold(pic.MIME, "image/png"):
		ext = "png"
	case strings.EqualFold(pic.MIME, "image/jpeg"), strings.EqualFold(pic.MIME, "image/jpg"):
		ext = "jpg"
	}

	name := "cover." + ext
	path := filepath.Join(outputDir, name)

	err := os.WriteFile(path, pic.Data, fileMode)
	if err != nil {
		return "", fmt.Errorf("writing cover to file: %w", err)
	}

	return path, nil
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
// If cue and track are non-nil, a VorbisComment metadata block is written with ALBUM (from cue Title),
// ARTIST (from track Performer or cue Performer), TITLE (track title),
// and TRACKNUMBER so Lidarr and others can identify the file.
func writeTrackFLAC( //nolint:funlen
	outputPath string,
	srcInfo *meta.StreamInfo,
	allFrames []flacFrame,
	startSample, endSample uint64,
	fileMode os.FileMode,
	blocks []*meta.Block,
) (uint64, error) {
	outFile, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fileMode)
	if err != nil {
		return 0, fmt.Errorf("creating output flac file: %w", err)
	}

	trackSamples := endSample - startSample

	// Create a new StreamInfo for this track.
	// BlockSizeMin/Max will be rewritten by the encoder on Close().
	// FrameSizeMin/Max are set to 0 (unknown) because the mewkiz encoder does
	// not track frame sizes and will not update them on Close(); copying the
	// source values would be wrong after splitting frames at track boundaries.
	trackInfo := &meta.StreamInfo{
		BlockSizeMin:  srcInfo.BlockSizeMin,
		BlockSizeMax:  srcInfo.BlockSizeMax,
		FrameSizeMin:  0,
		FrameSizeMax:  0,
		SampleRate:    srcInfo.SampleRate,
		NChannels:     srcInfo.NChannels,
		BitsPerSample: srcInfo.BitsPerSample,
		NSamples:      trackSamples,
	}

	enc, err := flac.NewEncoder(outFile, trackInfo, blocks...)
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

		offsetInFrame := int(clipStart - srcFrame.sampleStart)
		samplesToTake := int(clipEnd - clipStart)

		if samplesToTake <= 0 {
			continue
		}

		outFrame := buildOutputFrame(srcFrame.frame, offsetInFrame, samplesToTake)

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
// All output frames are created with HasFixedBlockSize=false (variable block size mode)
// regardless of the source stream's block size mode. This ensures a consistent encoding
// throughout the output file: mixing fixed-blocksize frames (which encode a frame number
// in the header) with variable-blocksize frames (which encode a sample position) produces
// an invalid FLAC stream that many decoders — including GStreamer's flacparse — will reject.
func buildOutputFrame(src *frame.Frame, offset, count int) *frame.Frame {
	// Always correlate to get proper L/R samples before slicing.
	// Correlate is a no-op when the frame is already in correlated form.
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
