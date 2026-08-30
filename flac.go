package xtractr

/* FLAC track splitting from a CUE sheet. */

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
)

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
		picturePaths, pictureBytes, err = writePicturesToFiles(xFile, xFile.OutputDir, pictures, xFile.FileMode)
		switch {
		case IsLimitError(err):
			return 0, nil, err
		case err != nil:
			xFile.Debugf("Error writing album art files: %s", err)
		}

		for _, p := range picturePaths {
			xFile.Debugf("Wrote album art: %s", p)
		}
	}

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
	held       *frame.Frame // last built frame, not yet written (see writeClip)
	outputPath string
	number     int
	start      uint64
	end        uint64
}

// minFLACBlockSize is the smallest block size (in samples) the FLAC format allows
// for any frame except the final frame of a stream (RFC 9639, section 4.1).
// maxFLACBlockSize is the largest block size a FLAC frame can hold.
const (
	minFLACBlockSize = 16
	maxFLACBlockSize = 65535
)

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
		// Close before unlink so Windows can remove the files; ExtractCUE
		// discards the file list on error, so leftovers would otherwise stay.
		splitter.closeOpen()
		splitter.removeFiles()

		return 0, nil, err
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
	outputPath := filepath.Join(s.xFile.OutputDir, formatTrackFilename(track, ".flac"))
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

	outFile, usedPath, err := openExtractFile(outputPath, s.xFile.FileMode)
	if err != nil {
		return nil, fmt.Errorf("creating output file for track %d: %w", track.Number, err)
	}

	enc, err := flac.NewEncoder(s.xFile.countedWriteSeeker(outFile), trackInfo, blocks...)
	if err != nil {
		_ = outFile.Close()
		_ = os.Remove(usedPath)

		return nil, fmt.Errorf("creating encoder for track %d: %w", track.Number, err)
	}

	err = s.xFile.countExtracted()
	if err != nil {
		_ = enc.Close()
		_ = os.Remove(usedPath)

		return nil, err
	}

	return &trackEncoder{
		enc:        enc,
		outputPath: usedPath,
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

		err := encoder.writeClip(parsed, offsetInFrame, samplesToTake)
		if err != nil {
			return fmt.Errorf("writing frame to track %d (%s): %w", encoder.number, encoder.outputPath, err)
		}
	}

	return nil
}

// writeClip builds an output frame from a clip of the source frame and writes it to
// the track. One frame is held back so a clip smaller than the FLAC minimum block
// size merges with its neighbor instead of becoming a spec-invalid tiny frame: a CUE
// boundary that falls near a source frame's edge otherwise produces a track whose
// first or last clip is under 16 samples, and the encoder records the smallest
// written frame as STREAMINFO's minimum block size, which strict parsers reject.
func (e *trackEncoder) writeClip(src *frame.Frame, offset, count int) error {
	newFrame := buildOutputFrame(src, offset, count)

	if e.held == nil {
		e.held = newFrame
		return nil
	}

	write, hold := balanceFrames(e.held, newFrame)
	e.held = hold

	if write == nil {
		return nil // the clips were concatenated into the held frame
	}

	err := e.enc.WriteFrame(write)
	if err != nil {
		return fmt.Errorf("encoding flac frame: %w", err)
	}

	return nil
}

// balanceFrames returns the frame to write now and the frame to hold for the next
// clip, sized so neither is smaller than the FLAC minimum block size. When the two
// fit in one frame it concatenates them (write is nil); otherwise it moves the
// smallest possible tail of the larger frame onto the smaller one. Total samples
// and their order are always preserved.
func balanceFrames(held, next *frame.Frame) (write, hold *frame.Frame) {
	heldSamples := held.Subframes[0].NSamples
	nextSamples := next.Subframes[0].NSamples
	combined := heldSamples + nextSamples

	switch {
	case heldSamples >= minFLACBlockSize && nextSamples >= minFLACBlockSize:
		// Both already valid; write the earlier frame and hold the later one.
		return held, next
	case combined <= maxFLACBlockSize:
		// Fits in one frame; write nothing and hold the concatenation.
		return nil, concatFrames(held, next)
	default:
		// Too large to concatenate. Move the tail of the larger frame onto the
		// smaller one so both meet the minimum block size.
		move := minFLACBlockSize - min(heldSamples, nextSamples)

		if heldSamples >= nextSamples {
			// held is larger: shrink held by its tail, prepend that tail to next.
			return clipFrame(held, 0, heldSamples-move, nil), clipFrame(next, 0, nextSamples, tailFrame(held, move))
		}

		// next is larger: shrink next by its tail, append held before next's head.
		return concatFrames(held, clipFrame(next, 0, move, nil)), clipFrame(next, move, nextSamples-move, nil)
	}
}

// concatFrames returns one frame holding held's samples followed by next's.
func concatFrames(held, next *frame.Frame) *frame.Frame {
	combined := held.Subframes[0].NSamples + next.Subframes[0].NSamples
	merged := &frame.Frame{Header: held.Header}
	merged.BlockSize = uint16(combined)
	merged.Subframes = make([]*frame.Subframe, len(held.Subframes))

	for channel := range held.Subframes {
		samples := make([]int32, 0, combined)
		samples = append(samples, held.Subframes[channel].Samples...)
		samples = append(samples, next.Subframes[channel].Samples...)

		merged.Subframes[channel] = &frame.Subframe{
			SubHeader: held.Subframes[channel].SubHeader,
			Samples:   samples,
			NSamples:  combined,
		}
	}

	return merged
}

// tailFrame returns a frame holding the last count samples of src.
func tailFrame(src *frame.Frame, count int) *frame.Frame {
	return clipFrame(src, src.Subframes[0].NSamples-count, count, nil)
}

// clipFrame returns a frame of count samples starting at offset. When prefix is
// non-nil, that many samples from the end of prefix are prepended first (used to
// move an earlier frame's tail onto the start of the next frame).
func clipFrame(src *frame.Frame, offset, count int, prefix *frame.Frame) *frame.Frame {
	total := count
	if prefix != nil {
		total += prefix.Subframes[0].NSamples
	}

	out := &frame.Frame{Header: src.Header}
	out.BlockSize = uint16(total)
	out.Subframes = make([]*frame.Subframe, len(src.Subframes))

	for channel := range src.Subframes {
		var samples []int32
		if prefix != nil {
			samples = append(samples, prefix.Subframes[channel].Samples...)
		}

		samples = append(samples, src.Subframes[channel].Samples[offset:offset+count]...)

		out.Subframes[channel] = &frame.Subframe{
			SubHeader: src.Subframes[channel].SubHeader,
			Samples:   samples,
			NSamples:  total,
		}
	}

	return out
}

// flushHeld writes the buffered frame, if any. Called when a track ends.
// A track shorter than the FLAC minimum block size has nothing to balance
// against, so the encoder would record a STREAMINFO minimum block size below
// 16 and strict parsers would reject the file. Refuse instead of writing a
// corrupt track.
func (e *trackEncoder) flushHeld() error {
	if e.held == nil {
		return nil
	}

	if e.held.Subframes[0].NSamples < minFLACBlockSize {
		samples := e.held.Subframes[0].NSamples
		e.held = nil

		return fmt.Errorf("%w (%d samples)", ErrTrackTooShort, samples)
	}

	err := e.enc.WriteFrame(e.held)
	e.held = nil

	if err != nil {
		return fmt.Errorf("encoding flac frame: %w", err)
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
	err := encoder.flushHeld()
	if err != nil {
		// The encoder is removed from s.open by the caller, so closeOpen cannot
		// reach it; close it here to avoid leaking the output descriptor.
		_ = encoder.enc.Close()
		_ = os.Remove(encoder.outputPath)

		return fmt.Errorf("writing final frame to track %d (%s): %w", encoder.number, encoder.outputPath, err)
	}

	err = encoder.enc.Close()
	if err != nil {
		_ = os.Remove(encoder.outputPath)

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

func (s *trackSplitter) removeFiles() {
	for _, path := range s.files {
		_ = os.Remove(path)
	}
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
func writePicturesToFiles(
	xFile *XFile,
	outputDir string,
	pictures []*meta.Picture,
	fileMode os.FileMode,
) ([]string, uint64, error) {
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

		err := xFile.writeExtractFile(path, pic.Data, fileMode)
		if err != nil {
			return paths, totalBytes, fmt.Errorf("writing %s: %w", name, err)
		}

		paths = append(paths, path)
		totalBytes += uint64(len(pic.Data))
	}

	return paths, totalBytes, nil
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
