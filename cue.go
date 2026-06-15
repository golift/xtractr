package xtractr

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

// ErrUTF16LengthInvalid is returned when the length of a UTF-16 encoded byte slice is not even.
var ErrUTF16LengthInvalid = errors.New("invalid UTF-16 length")

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
	// If the FILE line still does not match (e.g. O vs Ö), try the FLAC with the same base name as the CUE file.
	cueDir := filepath.Dir(xFile.FilePath)

	audioPath, err := resolveCueAudioPath(cueDir, cue.File, xFile.FilePath)
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

	// Write the CUE sheet into the output directory so the folder is self-contained
	// (tracks, art, and the exact split definition for archival and re-rip verification).
	cueBase := filepath.Base(xFile.FilePath)
	cueDest := filepath.Join(xFile.OutputDir, cueBase)

	writeErr := copyCueToOutput(xFile.FilePath, cueDest, xFile.FileMode)
	if writeErr != nil {
		xFile.Debugf("Copying CUE sheet to output: %s", writeErr)
	} else {
		files = append(files, cueDest)
		// Mark so recursion does not try to extract this copied CUE again.
		xFile.SkipOnRecursion = append(xFile.SkipOnRecursion, cueDest)
	}

	// The archive list includes both the CUE file and the FLAC file.
	archives = []string{xFile.FilePath, audioPath}

	return size, files, archives, nil
}

// parseCueSheetFile parses a CUE sheet from a file path and returns the sheet plus raw timestamps.
// It supports UTF-8, UTF-8 with BOM, and UTF-16 (LE/BE with BOM) encoded CUE files.
// TL;dr Some CUE sheets really suck.
//
//nolint:cyclop // tell me about it.
func parseCueSheetFile(path string) (*CueSheet, []cueTimestamp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening cue sheet: %w", err)
	}

	// Detect BOM and decode to UTF-8 so the scanner sees valid text.
	// UTF-16 LE file has BOM bytes FF FE -> LittleEndian 0xFEFF.
	// UTF-16 BE file has BOM bytes FE FF -> LittleEndian 0xFFFE.
	const (
		utf16LEBOM = 0xFEFF
		utf16BEBOM = 0xFFFE
	)

	var reader io.Reader

	if len(data) > 1 {
		switch bom := binary.LittleEndian.Uint16(data[:2]); bom {
		case utf16LEBOM:
			// UTF-16 little-endian; decode data[2:] as LE.
			decoded, errDec := decodeUTF16(data[2:], binary.LittleEndian)
			if errDec != nil {
				return nil, nil, fmt.Errorf("decoding UTF-16 LE cue sheet: %w", errDec)
			}

			reader = bytes.NewReader(decoded)
		case utf16BEBOM:
			// UTF-16 big-endian; decode data[2:] as BE.
			decoded, errDec := decodeUTF16(data[2:], binary.BigEndian)
			if errDec != nil {
				return nil, nil, fmt.Errorf("decoding UTF-16 BE cue sheet: %w", errDec)
			}

			reader = bytes.NewReader(decoded)
		}
	}

	if reader == nil {
		// No UTF-16 BOM; strip UTF-8 BOM if present.
		if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
			data = data[3:]
		}

		reader = bytes.NewReader(data)
	}

	return parseCueSheet(reader)
}

// decodeUTF16 decodes a UTF-16 encoded byte slice to UTF-8.
//
//nolint:mnd
func decodeUTF16(data []byte, order binary.ByteOrder) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, fmt.Errorf("%w: %d", ErrUTF16LengthInvalid, len(data))
	}

	u16 := make([]uint16, len(data)/2)
	for i := range u16 {
		u16[i] = order.Uint16(data[2*i:])
	}

	runes := utf16.Decode(u16)
	// Encode runes to UTF-8.
	buf := make([]byte, 0, len(runes)*utf8.UTFMax)
	for _, r := range runes {
		buf = utf8.AppendRune(buf, r)
	}

	return buf, nil
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
// If the FILE line does not match any file (e.g. encoding or O vs Ö), it tries the FLAC with the same
// base name as the CUE file (e.g. Artist - Album.cue -> Artist - Album.flac).
func resolveCueAudioPath(cueDir, cueFile, cueFilePath string) (string, error) {
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

	// Fallback: try the FLAC with the same base name as the CUE file (handles O vs Ö, encoding mismatches).
	baseNoExt := strings.TrimSuffix(filepath.Base(cueFilePath), filepath.Ext(cueFilePath))
	fallbackPath := filepath.Join(cueDir, baseNoExt+".flac")

	_, err = os.Stat(fallbackPath)
	if err == nil {
		return fallbackPath, nil
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
// It streams frames one at a time to avoid loading the entire FLAC into memory.
//
//nolint:cyclop
func splitFLAC(xFile *XFile, audioPath string, cue *CueSheet, timestamps []cueTimestamp) (uint64, []string, error) {
	// Parse metadata only (no audio frames loaded into memory).
	flacMeta, err := readFLACMetadata(audioPath)
	if err != nil {
		return 0, nil, err
	}

	streamInfo := flacMeta.Info
	pictures := flacMeta.Pictures
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
		picturePaths []string
		pictureBytes uint64
	)

	if len(pictures) > 0 {
		picturePaths, pictureBytes, err = writePicturesToFiles(xFile.OutputDir, pictures, xFile.FileMode)
		if err != nil {
			xFile.Debugf("Error writing album art files: %s", err)
		}

		for _, p := range picturePaths {
			xFile.Debugf("Wrote album art: %s", p)
		}
	}

	defer xFile.newProgress(0, 0, len(cue.Tracks)).done()

	// Stream frames one at a time, writing each to the appropriate track encoder.
	totalSize, files, err := streamTracksFLAC(xFile, audioPath, cue, trackStarts, trackEnds, streamInfo, flacMeta)
	if err != nil {
		return 0, nil, err
	}

	if len(picturePaths) > 0 {
		return totalSize + pictureBytes, append(files, picturePaths...), nil
	}

	return totalSize, files, nil
}

// trackEncoder holds an open encoder for a single output track during streaming.
type trackEncoder struct {
	enc        *flac.Encoder
	outputPath string
	number     int
	start      uint64
	end        uint64
}

// trackSplitter streams source FLAC frames into per-track encoders. It opens a track
// encoder only when the stream reaches that track and closes it as soon as the stream
// passes the track's end. This bounds the number of simultaneously open files to the
// few adjacent tracks a single frame can overlap (normally one or two) regardless of
// how many tracks the CUE defines, so large box sets do not exhaust the process
// file-descriptor limit. Only one decoded frame is held in memory at a time.
type trackSplitter struct {
	xFile       *XFile
	cue         *CueSheet
	trackStarts []uint64
	trackEnds   []uint64
	streamInfo  *meta.StreamInfo
	flacMeta    *flacMetadata
	open        []*trackEncoder // currently-open encoders, in track order
	nextTrack   int             // index of the next track not yet opened
	files       []string        // output paths, in track order, for tracks opened so far
	totalSize   uint64
}

// streamTracksFLAC streams FLAC frames one at a time, writing each frame to the
// appropriate track encoder. Only one frame is in memory at a time, keeping peak
// memory at ~64KB instead of loading the entire FLAC (~1GB+ for 24-bit/96kHz).
func streamTracksFLAC(
	xFile *XFile,
	audioPath string,
	cue *CueSheet,
	trackStarts []uint64,
	trackEnds []uint64,
	streamInfo *meta.StreamInfo,
	flacMeta *flacMetadata,
) (uint64, []string, error) {
	audioFile, err := os.Open(audioPath)
	if err != nil {
		return 0, nil, fmt.Errorf("opening flac for streaming: %w", err)
	}
	defer audioFile.Close()

	stream, err := flac.Parse(audioFile)
	if err != nil {
		return 0, nil, fmt.Errorf("parsing flac for streaming: %w", err)
	}

	splitter := &trackSplitter{
		xFile:       xFile,
		cue:         cue,
		trackStarts: trackStarts,
		trackEnds:   trackEnds,
		streamInfo:  streamInfo,
		flacMeta:    flacMeta,
		open:        make([]*trackEncoder, 0, 2), //nolint:mnd // a frame overlaps at most ~2 tracks.
		files:       make([]string, 0, len(cue.Tracks)),
	}
	// Belt-and-suspenders: close any still-open encoders if we return early on error.
	defer splitter.closeOpen()

	err = splitter.run(stream)
	if err != nil {
		return splitter.totalSize, splitter.files, err
	}

	return splitter.totalSize, splitter.files, nil
}

// run reads frames until EOF, routing each to the encoders it overlaps and opening
// and closing track encoders as the stream position crosses their boundaries.
func (s *trackSplitter) run(stream *flac.Stream) error {
	var samplePos uint64

	for {
		parsed, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			return s.finishAll()
		}

		if err != nil {
			return fmt.Errorf("parsing flac frame: %w", err)
		}

		frameStart := samplePos
		frameEnd := samplePos + uint64(parsed.Subframes[0].NSamples)
		samplePos = frameEnd

		err = s.processFrame(parsed, frameStart, frameEnd)
		if err != nil {
			return err
		}
	}
}

// processFrame opens any tracks this frame reaches, writes the frame's overlapping
// portion to every open track, then closes any track that ends within this frame.
func (s *trackSplitter) processFrame(parsed *frame.Frame, frameStart, frameEnd uint64) error {
	err := s.openReachedTracks(frameEnd)
	if err != nil {
		return err
	}

	err = s.writeFrame(parsed, frameStart, frameEnd)
	if err != nil {
		return err
	}

	return s.closeFinishedTracks(frameEnd)
}

// openReachedTracks opens encoders for every not-yet-opened track whose start falls
// before frameEnd (i.e. the stream has reached it). Zero-length tracks are skipped.
func (s *trackSplitter) openReachedTracks(frameEnd uint64) error {
	for s.nextTrack < len(s.cue.Tracks) && s.trackStarts[s.nextTrack] < frameEnd {
		idx := s.nextTrack
		s.nextTrack++

		if s.trackEnds[idx] <= s.trackStarts[idx] {
			continue // skip zero-length tracks
		}

		encoder, err := s.openEncoder(idx)
		if err != nil {
			return err
		}

		s.open = append(s.open, encoder)
		s.files = append(s.files, encoder.outputPath)
	}

	return nil
}

// openEncoder creates the output file and FLAC encoder for a single track.
func (s *trackSplitter) openEncoder(idx int) (*trackEncoder, error) {
	track := &s.cue.Tracks[idx]
	outputPath := filepath.Join(s.xFile.OutputDir, formatTrackFilename(track))
	blocks := buildTrackMetadataBlocks(s.cue, track, s.flacMeta)

	trackInfo := &meta.StreamInfo{
		BlockSizeMin:  s.streamInfo.BlockSizeMin,
		BlockSizeMax:  s.streamInfo.BlockSizeMax,
		FrameSizeMin:  0,
		FrameSizeMax:  0,
		SampleRate:    s.streamInfo.SampleRate,
		NChannels:     s.streamInfo.NChannels,
		BitsPerSample: s.streamInfo.BitsPerSample,
		NSamples:      s.trackEnds[idx] - s.trackStarts[idx],
	}

	outFile, err := os.OpenFile(outputPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, s.xFile.FileMode)
	if err != nil {
		return nil, fmt.Errorf("creating output file for track %d: %w", track.Number, err)
	}

	enc, err := flac.NewEncoder(outFile, trackInfo, blocks...)
	if err != nil {
		_ = outFile.Close()
		return nil, fmt.Errorf("creating encoder for track %d: %w", track.Number, err)
	}

	return &trackEncoder{
		enc:        enc,
		outputPath: outputPath,
		number:     track.Number,
		start:      s.trackStarts[idx],
		end:        s.trackEnds[idx],
	}, nil
}

// writeFrame writes the portion of one decoded frame that belongs to each currently
// open track. A frame that straddles a track boundary is clipped and written to both
// adjacent tracks.
func (s *trackSplitter) writeFrame(parsed *frame.Frame, frameStart, frameEnd uint64) error {
	for _, encoder := range s.open {
		if frameEnd <= encoder.start || frameStart >= encoder.end {
			continue // frame is entirely outside this track
		}

		clipStart := max(frameStart, encoder.start)
		clipEnd := min(frameEnd, encoder.end)
		offsetInFrame := int(clipStart - frameStart)
		samplesToTake := int(clipEnd - clipStart)

		if samplesToTake <= 0 {
			continue
		}

		err := encoder.enc.WriteFrame(buildOutputFrame(parsed, offsetInFrame, samplesToTake))
		if err != nil {
			return fmt.Errorf("writing frame to track %d (%s): %w", encoder.number, encoder.outputPath, err)
		}
	}

	return nil
}

// closeFinishedTracks finalizes and drops every open encoder whose track ends at or
// before frameEnd, freeing its file descriptor as soon as the stream passes it.
func (s *trackSplitter) closeFinishedTracks(frameEnd uint64) error {
	remaining := s.open[:0]

	for idx, encoder := range s.open {
		if encoder.end > frameEnd {
			remaining = append(remaining, encoder)
			continue
		}

		err := s.finalize(encoder)
		if err != nil {
			// Keep tracks not yet processed (excluding the failed one) for cleanup.
			s.open = append(remaining, s.open[idx+1:]...)
			return err
		}
	}

	s.open = remaining

	return nil
}

// finishAll finalizes every still-open encoder; called once the stream hits EOF.
func (s *trackSplitter) finishAll() error {
	for idx, encoder := range s.open {
		err := s.finalize(encoder)
		if err != nil {
			s.open = s.open[idx+1:]
			return err
		}
	}

	s.open = nil

	return nil
}

// finalize closes a track encoder (flushing the FLAC stream) and records its size.
func (s *trackSplitter) finalize(encoder *trackEncoder) error {
	err := encoder.enc.Close()
	if err != nil {
		return fmt.Errorf("closing track %d encoder (%s): %w", encoder.number, encoder.outputPath, err)
	}

	stat, err := os.Stat(encoder.outputPath)
	if err != nil {
		return fmt.Errorf("stat output file for track %d (%s): %w", encoder.number, encoder.outputPath, err)
	}

	size := uint64(stat.Size())
	s.totalSize += size

	s.xFile.Debugf("Wrote track %d: %s (%d bytes)", encoder.number, encoder.outputPath, size)

	return nil
}

// closeOpen closes all still-open track encoders, ignoring errors (cleanup on failure).
func (s *trackSplitter) closeOpen() {
	for _, encoder := range s.open {
		if encoder.enc != nil {
			_ = encoder.enc.Close()
		}
	}

	s.open = nil
}

// flacMetadata holds metadata read from a FLAC file for use when splitting by CUE.
type flacMetadata struct {
	Info          *meta.StreamInfo
	Pictures      []*meta.Picture
	VorbisComment *meta.VorbisComment // source tags to merge into each track (GENRE, DATE, etc.)
	OtherBlocks   []*meta.Block       // Application, CueSheet — copied into each track
}

// readFLACMetadata opens a FLAC file, parses only metadata blocks (no audio frames),
// and closes the file. Audio frames are streamed separately by streamTracksFLAC.
func readFLACMetadata(audioPath string) (*flacMetadata, error) { //nolint:cyclop
	file, err := os.Open(audioPath)
	if err != nil {
		return nil, fmt.Errorf("opening flac file: %w", err)
	}
	defer file.Close()

	stream, err := flac.Parse(file)
	if err != nil {
		return nil, fmt.Errorf("parsing flac file: %w", err)
	}

	flacMeta := &flacMetadata{
		Info: stream.Info,
	}

	for _, blk := range stream.Blocks {
		switch blk.Type { //nolint:exhaustive // we do not need them all here.
		case meta.TypePicture:
			if pic, ok := blk.Body.(*meta.Picture); ok {
				flacMeta.Pictures = append(flacMeta.Pictures, pic)
			}
		case meta.TypeVorbisComment:
			if flacMeta.VorbisComment == nil && blk.Body != nil {
				if vc, ok := blk.Body.(*meta.VorbisComment); ok {
					flacMeta.VorbisComment = vc
				}
			}
		case meta.TypeApplication, meta.TypeCueSheet:
			flacMeta.OtherBlocks = append(flacMeta.OtherBlocks, blk)
		}
	}

	return flacMeta, nil
}

// vorbisTagsFromCUE are tag keys we set from the CUE sheet; we do not overwrite these from source.
func vorbisTagsFromCUE() map[string]bool {
	return map[string]bool{
		"ALBUM": true, "ARTIST": true, "TITLE": true, "TRACKNUMBER": true,
	}
}

// vorbisTagsToMergeFromSource are tag keys we copy from the source FLAC when present
// (genre, date, album artist, etc.) so split tracks retain full metadata.
func vorbisTagsToMergeFromSource() map[string]bool {
	return map[string]bool{
		"ALBUMARTIST": true, "GENRE": true, "DATE": true, "COMMENT": true,
		"COMPOSER": true, "DISCNUMBER": true, "DISCTOTAL": true, "BPM": true,
		"LABEL": true, "CATALOG": true, "ISRC": true, "PUBLISHER": true,
		"COPYRIGHT": true, "DESCRIPTION": true, "ENCODED-BY": true,
	}
}

// buildVorbisCommentBlock returns a FLAC metadata block with ALBUM, ARTIST, TITLE, TRACKNUMBER
// from the CUE sheet and track, and merges in source FLAC tags (GENRE, DATE, ALBUMARTIST, etc.)
// when present so split tracks retain full metadata for players and libraries.
//
//nolint:cyclop
func buildVorbisCommentBlock(cue *CueSheet, track *CueTrack, sourceVorbis *meta.VorbisComment) *meta.Block {
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

	haveKey := map[string]bool{}
	for _, pair := range tags {
		haveKey[strings.ToUpper(pair[0])] = true
	}

	// Copy source VorbisComment tags that are not in the CUE sheet.
	if sourceVorbis != nil {
		for _, pair := range sourceVorbis.Tags {
			tagKey := strings.ToUpper(pair[0])
			if vorbisTagsFromCUE()[tagKey] || haveKey[tagKey] {
				continue
			}

			if vorbisTagsToMergeFromSource()[tagKey] {
				tags = append(tags, [2]string{pair[0], pair[1]})
				haveKey[tagKey] = true
			}
		}
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

// buildTrackMetadataBlocks returns metadata blocks for a split track: merged VorbisComment,
// copied Application/CueSheet blocks (if any), and all Picture blocks. The last block has
// IsLast set so the FLAC encoder writes the metadata block chain correctly.
func buildTrackMetadataBlocks(cue *CueSheet, track *CueTrack, flacMeta *flacMetadata) []*meta.Block {
	blocks := make([]*meta.Block, 0, len(flacMeta.OtherBlocks)+len(flacMeta.Pictures)+1)

	if cue != nil && track != nil {
		blocks = append(blocks, buildVorbisCommentBlock(cue, track, flacMeta.VorbisComment))
	}

	for _, blk := range flacMeta.OtherBlocks {
		// Copy block with IsLast false; encoder will see more blocks after.
		blocks = append(blocks, &meta.Block{
			Header: meta.Header{Type: blk.Type, Length: blk.Length, IsLast: false},
			Body:   blk.Body,
		})
	}

	for _, pic := range flacMeta.Pictures {
		blocks = append(blocks, &meta.Block{
			Header: meta.Header{Type: meta.TypePicture, Length: 1, IsLast: false},
			Body:   pic,
		})
	}

	if len(blocks) > 0 {
		blocks[len(blocks)-1].IsLast = true
	}

	return blocks
}

// pictureTypeNames maps FLAC/ID3v2 APIC picture types to short basenames for files.
// Type 3 (front cover) uses "cover" so the main art file stays cover.png/jpg.
func pictureTypeNames() map[uint32]string {
	return map[uint32]string{
		0: "other", 1: "file_icon", 2: "file_icon_other", 3: "cover", 4: "cover_back",
		5: "leaflet", 6: "media", 7: "lead_artist", 8: "artist", 9: "conductor",
		10: "band", 11: "composer", 12: "lyricist", 13: "recording_location",
		14: "during_recording", 15: "during_performance", 16: "movie", 17: "fish",
		18: "illustration", 19: "band_logo", 20: "publisher_logo",
	}
}

// writePicturesToFiles writes all picture blocks to files in outputDir. Front cover
// (type 3) is named cover.<ext>; others use the picture type (e.g. cover_back.png).
// Returns written paths, total bytes written, and any error from the first failed write.
func writePicturesToFiles(outputDir string, pictures []*meta.Picture, fileMode os.FileMode) ([]string, uint64, error) {
	typeCount := make(map[string]int)
	paths := make([]string, 0, len(pictures))
	totalBytes := uint64(0)

	for _, pic := range pictures {
		ext := "bin"

		switch {
		case strings.EqualFold(pic.MIME, "image/png"):
			ext = "png"
		case strings.EqualFold(pic.MIME, "image/jpeg"), strings.EqualFold(pic.MIME, "image/jpg"):
			ext = "jpg"
		}

		base := pictureTypeNames()[pic.Type]
		if base == "" {
			base = "image_" + strconv.FormatUint(uint64(pic.Type), 10)
		}

		typeCount[base]++
		name := base

		if typeCount[base] > 1 {
			name = base + "_" + strconv.Itoa(typeCount[base])
		}

		name += "." + ext
		path := filepath.Join(outputDir, name)

		err := os.WriteFile(path, pic.Data, fileMode)
		if err != nil {
			return paths, totalBytes, fmt.Errorf("writing %s: %w", name, err)
		}

		paths = append(paths, path)
		totalBytes += uint64(len(pic.Data))
	}

	return paths, totalBytes, nil
}

// copyCueToOutput copies the CUE sheet file into the output directory so the
// extracted folder contains tracks, album art, and the CUE for verification/archival.
func copyCueToOutput(srcPath, destPath string, fileMode os.FileMode) error {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading cue sheet: %w", err)
	}

	err = os.WriteFile(destPath, data, fileMode) //nolint:gosec // ffs.
	if err != nil {
		return fmt.Errorf("writing cue sheet: %w", err)
	}

	return nil
}

// buildOutputFrame creates a new frame with a subset of samples from the source frame.
// All output frames are created with HasFixedBlockSize=false (variable block size mode)
// regardless of the source stream's block size mode. This ensures a consistent encoding
// throughout the output file: mixing fixed-blocksize frames (which encode a frame number
// in the header) with variable-blocksize frames (which encode a sample position) produces
// an invalid FLAC stream that many decoders — including GStreamer's flacparse — will reject.
func buildOutputFrame(src *frame.Frame, offset, count int) *frame.Frame {
	// The decoder's Frame.Parse already correlates subframes to independent L/R
	// samples (see mewkiz/flac frame.Parse), so src.Subframes hold actual L/R here.
	// We must NOT correlate again: doing so double-transforms inter-channel
	// decorrelated frames (mid/side, left/side, right/side) and corrupts the output
	// (notably the right channel) for every such frame. The encoder's WriteFrame
	// re-applies decorrelation based on Header.Channels, so we pass L/R straight
	// through. ref: Unpackerr/unpackerr#634.
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
// It normalizes smart/curly quotes to ASCII so tools like Lidarr can find files reliably.
func sanitizeFilename(name string) string {
	// Normalize smart quotes and curly quotes to ASCII (fixes Lidarr "could not find file" when CUE has U+2019 etc).
	name = strings.ReplaceAll(name, "\u2018", "'")  // LEFT SINGLE QUOTATION MARK
	name = strings.ReplaceAll(name, "\u2019", "'")  // RIGHT SINGLE QUOTATION MARK
	name = strings.ReplaceAll(name, "\u201C", "\"") // LEFT DOUBLE QUOTATION MARK
	name = strings.ReplaceAll(name, "\u201D", "\"") // RIGHT DOUBLE QUOTATION MARK
	// Remove other characters that are problematic in filenames or for downstream tools.
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
	name = replacer.Replace(name)

	// Strip control characters and Unicode replacement character.
	var data strings.Builder
	data.Grow(len(name))

	for _, r := range name {
		if r == '\uFFFD' || r < 32 || r == 127 {
			continue
		}

		data.WriteRune(r)
	}

	return data.String()
}
