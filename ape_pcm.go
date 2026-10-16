package xtractr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golift.io/ape"
)

const bitsPerByte = 8

// audioFormat resolves Output. Empty and "ape" stay APE.
func (o APEOpts) audioFormat() (AudioFormat, error) {
	format := AudioFormat(strings.ToLower(strings.TrimSpace(string(o.Output))))

	switch format {
	case "", AudioFormatAPE:
		return AudioFormatAPE, nil
	case AudioFormatWAV, AudioFormatFLAC:
		return format, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedAPEOutput, o.Output)
	}
}

func (f AudioFormat) ext() string {
	return "." + string(f)
}

// ConvertAPE decodes the APE file at xFile.FilePath and writes one audio file
// into xFile.OutputDir. The name is the source base name plus the extension
// for xFile.Output. An empty Output stays APE. Compression applies only then.
//
// No CUE sheet is read. ExtractCUE uses the same Output and Compression fields
// when it splits an APE image into tracks. The source file is not replaced.
func ConvertAPE(xFile *XFile) (size uint64, files []string, err error) {
	format, err := xFile.audioFormat()
	if err != nil {
		return 0, nil, err
	}

	defer xFile.newProgress(0, archiveFileSize(xFile.FilePath), 1).done()

	return streamAPE(xFile, xFile.FilePath, format, nil, nil)
}

// splitAPEPlayable decodes the image and writes one file per CUE track, cut
// on the cue sample. Output selects APE, WAV, or FLAC. Files the decoder
// cannot read stay on the frame-copy path only when the output is APE.
func splitAPEPlayable(
	xFile *XFile,
	audioPath string,
	cue *CueSheet,
	timestamps []cueTimestamp,
) (uint64, []string, error) {
	format, err := xFile.audioFormat()
	if err != nil {
		return 0, nil, err
	}

	src, dec, err := openAPEDecoder(xFile, audioPath)
	if err != nil {
		if format != AudioFormatAPE || !errors.Is(err, errAPEDecode) {
			return 0, nil, err
		}

		xFile.Debugf("APE decode failed, copying frames: %v", err)

		return splitAPE(xFile, audioPath, cue, timestamps)
	}

	defer func() { _ = src.Close() }()

	return streamDecoded(xFile, audioPath, dec, format, cue, timestamps)
}

var errAPEDecode = errors.New("decoding ape")

func openAPEDecoder(xFile *XFile, audioPath string) (*os.File, *ape.Decoder, error) {
	src, err := os.Open(audioPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening ape: %w", err)
	}

	dec, err := ape.NewDecoder(xFile.countingReadSeeker(src))
	if err != nil {
		_ = src.Close()

		return nil, nil, fmt.Errorf("%w: %w", errAPEDecode, err)
	}

	return src, dec, nil
}

func streamAPE(
	xFile *XFile,
	audioPath string,
	format AudioFormat,
	cue *CueSheet,
	timestamps []cueTimestamp,
) (uint64, []string, error) {
	src, dec, err := openAPEDecoder(xFile, audioPath)
	if err != nil {
		return 0, nil, err
	}

	defer func() { _ = src.Close() }()

	return streamDecoded(xFile, audioPath, dec, format, cue, timestamps)
}

func streamDecoded(
	xFile *XFile,
	audioPath string,
	dec *ape.Decoder,
	format AudioFormat,
	cue *CueSheet,
	timestamps []cueTimestamp,
) (uint64, []string, error) {
	xFile.Printf("Decoding %s", audioPath)

	err := os.MkdirAll(xFile.OutputDir, xFile.dirMode())
	if err != nil {
		return 0, nil, fmt.Errorf("creating output directory: %w", err)
	}

	spans, err := apeOutputSpans(dec, cue, timestamps)
	if err != nil {
		return 0, nil, err
	}

	streamer := &apeStreamer{
		xFile:  xFile,
		src:    audioPath,
		dec:    dec,
		format: format,
		cue:    cue,
		spans:  spans,
		align:  dec.Stream().Channels * dec.Stream().Bits / bitsPerByte,
	}

	return streamer.run()
}

type sampleSpan struct {
	start uint64
	end   uint64
}

func apeOutputSpans(dec *ape.Decoder, cue *CueSheet, timestamps []cueTimestamp) ([]sampleSpan, error) {
	total := uint64(dec.Samples())
	if dec.Stream().SampleRate <= 0 || dec.Stream().Channels <= 0 || dec.Stream().Bits <= 0 || total == 0 {
		return nil, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	if cue == nil {
		return []sampleSpan{{end: total}}, nil
	}

	spans := make([]sampleSpan, len(cue.Tracks))
	rate := uint32(dec.Stream().SampleRate)

	for idx := range spans {
		span, err := cueSampleSpan(idx, timestamps, total, rate)
		if err != nil {
			return nil, err
		}

		spans[idx] = span
	}

	return spans, nil
}

func cueSampleSpan(idx int, timestamps []cueTimestamp, total uint64, rate uint32) (sampleSpan, error) {
	start := uint64(0)
	if idx > 0 && idx < len(timestamps) {
		start = timestamps[idx].toSamples(rate)
	}

	end := total
	if idx+1 < len(timestamps) {
		end = timestamps[idx+1].toSamples(rate)
	}

	if start > end || end > total {
		return sampleSpan{}, fmt.Errorf("%w: sample range %d:%d of %d", ErrUnsupportedAudio, start, end, total)
	}

	return sampleSpan{start: start, end: end}, nil
}

type apeStreamer struct {
	xFile  *XFile
	src    string
	dec    *ape.Decoder
	format AudioFormat
	cue    *CueSheet
	spans  []sampleSpan
	align  int
	idx    int
	cur    *pcmOut
	size   uint64
	files  []string
}

func (s *apeStreamer) run() (uint64, []string, error) {
	if s.align <= 0 {
		return 0, nil, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	pos := uint64(0)

	for {
		frame, err := s.dec.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return s.fail(fmt.Errorf("decoding ape: %w", err))
		}

		pos, err = s.writeFrame(frame, pos)
		if err != nil {
			return s.fail(err)
		}
	}

	if s.cur != nil {
		err := s.finishTrack()
		if err != nil {
			return s.fail(err)
		}
	}

	return s.size, s.files, nil
}

// fail drops the open track and every track already written. ExtractCUE
// discards the returned file list on error, so those files would otherwise stay.
func (s *apeStreamer) fail(err error) (uint64, []string, error) {
	if s.cur != nil {
		s.cur.abort()
		s.cur = nil
	}

	for _, path := range s.files {
		s.xFile.uncountExtracted()

		_ = os.Remove(path)
	}

	return 0, nil, err
}

func (s *apeStreamer) writeFrame(frame []byte, frameStart uint64) (uint64, error) {
	if len(frame)%s.align != 0 {
		return frameStart, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	end := frameStart + uint64(len(frame)/s.align)
	cursor := frameStart

	for s.idx < len(s.spans) && cursor < end {
		span := s.spans[s.idx]
		if end <= span.start {
			break
		}

		if cursor >= span.end {
			err := s.finishOpen()
			if err != nil {
				return cursor, err
			}

			s.idx++

			continue
		}

		err := s.writeSpan(frame, frameStart, &cursor, end, span)
		if err != nil {
			return cursor, err
		}
	}

	return end, nil
}

func (s *apeStreamer) writeSpan(frame []byte, frameStart uint64, cursor *uint64, end uint64, span sampleSpan) error {
	from := max(span.start, *cursor)
	stop := min(span.end, end)

	if s.cur == nil {
		err := s.openTrack(span)
		if err != nil {
			return err
		}
	}

	chunk := frame[int(from-frameStart)*s.align : int(stop-frameStart)*s.align]

	err := s.cur.write(chunk)
	if err != nil {
		return err
	}

	*cursor = stop
	if stop != span.end {
		return nil
	}

	err = s.finishTrack()
	if err != nil {
		return err
	}

	s.idx++

	return nil
}

func (s *apeStreamer) finishOpen() error {
	if s.cur == nil {
		return nil
	}

	return s.finishTrack()
}

func (s *apeStreamer) openTrack(span sampleSpan) error {
	samples := span.end - span.start
	if uint64(int(samples)) != samples {
		return fmt.Errorf("%w: track too long", ErrUnsupportedAudio)
	}

	name, tags := s.trackOutput()
	outputPath := filepath.Join(s.xFile.OutputDir, name)

	out, err := openPCMOut(s.xFile, s.src, outputPath, s.dec.Stream(), s.format, tags, int(samples))
	if err != nil {
		return fmt.Errorf("ape track %d: %w", s.trackNumber(), err)
	}

	s.cur = out

	return nil
}

func (s *apeStreamer) trackOutput() (string, map[string]string) {
	if s.cue == nil {
		base := strings.TrimSuffix(filepath.Base(s.src), filepath.Ext(s.src))

		return base + s.format.ext(), nil
	}

	track := &s.cue.Tracks[s.idx]

	return formatTrackFilename(track, s.format.ext()), apeTrackTags(s.cue, track)
}

func (s *apeStreamer) trackNumber() int {
	if s.cue == nil || s.idx >= len(s.cue.Tracks) {
		return s.idx + 1
	}

	return s.cue.Tracks[s.idx].Number
}

func (s *apeStreamer) finishTrack() error {
	out := s.cur
	s.cur = nil

	size, path, err := out.Close()
	if err != nil {
		return fmt.Errorf("writing %s track %d: %w", s.format, s.trackNumber(), err)
	}

	s.size += size
	s.files = append(s.files, path)
	s.xFile.Debugf("Wrote %s track %d: %s (%d bytes)", s.format, s.trackNumber(), path, size)

	return nil
}

type pcmOut struct {
	xFile *XFile
	file  *os.File
	path  string
	sink  pcmSink
}

func openPCMOut(
	xFile *XFile,
	src, outputPath string,
	stream ape.Stream,
	format AudioFormat,
	tags map[string]string,
	samples int,
) (*pcmOut, error) {
	if replacesSource(src, outputPath) {
		return nil, fmt.Errorf("%w: %s", ErrAPEOverwrite, outputPath)
	}

	outFile, usedPath, err := openExtractFile(outputPath, xFile.FileMode)
	if err != nil {
		return nil, fmt.Errorf("creating output file: %w", err)
	}

	err = xFile.countExtracted()
	if err != nil {
		_ = outFile.Close()
		_ = os.Remove(usedPath)

		return nil, err
	}

	dst := xFile.countedWriteSeeker(outFile)

	var sink pcmSink

	switch format {
	case AudioFormatWAV:
		sink, err = newWAVSink(dst, stream, tags, samples)
	case AudioFormatFLAC:
		sink, err = newFLACSink(dst, stream, tags, samples)
	case AudioFormatAPE:
		sink, err = newAPESink(dst, stream, tags, samples, xFile.Compression)
	default:
		err = fmt.Errorf("%w: %s", ErrUnsupportedAPEOutput, format)
	}

	if err != nil {
		xFile.uncountExtracted()

		_ = outFile.Close()
		_ = os.Remove(usedPath)

		return nil, err
	}

	return &pcmOut{xFile: xFile, file: outFile, path: usedPath, sink: sink}, nil
}

func (o *pcmOut) Close() (size uint64, path string, err error) {
	err = o.sink.finish()

	closeErr := o.file.Close()
	o.file = nil

	if err == nil {
		err = closeErr
	}

	if err != nil {
		o.xFile.uncountExtracted()
		_ = os.Remove(o.path)

		return 0, o.path, err
	}

	info, err := os.Stat(o.path)
	if err != nil {
		return 0, o.path, fmt.Errorf("stat encoded audio: %w", err)
	}

	return uint64(info.Size()), o.path, nil
}

func (o *pcmOut) write(pcm []byte) error {
	return o.sink.write(pcm)
}

func (o *pcmOut) abort() {
	if o == nil || o.file == nil {
		return
	}

	_ = o.file.Close()
	o.file = nil
	o.xFile.uncountExtracted()
	_ = os.Remove(o.path)
}

func replacesSource(src, dst string) bool {
	absSrc, errSrc := filepath.Abs(src)
	absDst, errDst := filepath.Abs(dst)

	if errSrc != nil || errDst != nil {
		return false
	}

	if samePath(absSrc, absDst) {
		return true
	}

	srcInfo, errSrc := os.Stat(src)
	dstInfo, errDst := os.Stat(dst)

	return errSrc == nil && errDst == nil && os.SameFile(srcInfo, dstInfo)
}

func samePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}

	return left == right
}

// apeEncodeLevel turns an unset APEOpts.Compression into normal. The codec
// treats 0 as fast, and this package's zero value is normal instead.
func apeEncodeLevel(compression int) ape.Compression {
	if compression == 0 {
		return ape.CompressionNormal
	}

	return ape.Compression(compression)
}

func apeTrackTags(cue *CueSheet, track *CueTrack) map[string]string {
	tags := map[string]string{}

	artist := track.Performer
	if artist == "" {
		artist = cue.Performer
	}

	if artist != "" {
		tags["Artist"] = artist
	}

	if cue.Title != "" {
		tags["Album"] = cue.Title
	}

	if track.Title != "" {
		tags["Title"] = track.Title
	}

	if track.Number > 0 {
		tags["Track"] = strconv.Itoa(track.Number)
	}

	if len(tags) == 0 {
		return nil
	}

	return tags
}
