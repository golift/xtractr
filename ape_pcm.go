package xtractr

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
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
// when it splits an APE image into tracks.
func ConvertAPE(xFile *XFile) (size uint64, files []string, err error) {
	format, err := xFile.audioFormat()
	if err != nil {
		return 0, nil, err
	}

	defer xFile.newProgress(0, archiveFileSize(xFile.FilePath), 1).done()

	xFile.Printf("Decoding %s", xFile.FilePath)

	pcm, stream, err := ape.DecodeFile(xFile.FilePath)
	if err != nil {
		return 0, nil, fmt.Errorf("decoding ape: %w", err)
	}

	err = os.MkdirAll(xFile.OutputDir, xFile.DirMode)
	if err != nil {
		return 0, nil, fmt.Errorf("creating output directory: %w", err)
	}

	base := strings.TrimSuffix(filepath.Base(xFile.FilePath), filepath.Ext(xFile.FilePath))
	outputPath := filepath.Join(xFile.OutputDir, base+format.ext())

	size, usedPath, err := writeDecodedAudioFile(xFile, outputPath, pcm, stream, format, nil)
	if err != nil {
		return 0, nil, err
	}

	xFile.Debugf("Wrote %s: %s (%d bytes)", format, usedPath, size)

	return size, []string{usedPath}, nil
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

	xFile.Printf("Decoding %s", audioPath)

	pcm, stream, err := ape.DecodeFile(audioPath)
	if err != nil {
		if format != AudioFormatAPE {
			return 0, nil, fmt.Errorf("decoding ape: %w", err)
		}

		xFile.Debugf("APE decode failed, copying frames: %v", err)

		return splitAPE(xFile, audioPath, cue, timestamps)
	}

	return writeDecodedAPETracks(xFile, pcm, stream, cue, timestamps, format)
}

func writeDecodedAPETracks(
	xFile *XFile,
	pcm []byte,
	stream ape.Stream,
	cue *CueSheet,
	timestamps []cueTimestamp,
	format AudioFormat,
) (uint64, []string, error) {
	align := stream.Channels * stream.Bits / bitsPerByte
	if align <= 0 || stream.SampleRate <= 0 || len(pcm)%align != 0 {
		return 0, nil, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	err := os.MkdirAll(xFile.OutputDir, xFile.DirMode)
	if err != nil {
		return 0, nil, fmt.Errorf("creating output directory: %w", err)
	}

	totalSamples := uint64(len(pcm) / align)
	rate := uint32(stream.SampleRate)

	var (
		totalSize uint64
		files     = make([]string, 0, len(cue.Tracks))
	)

	for idx := range cue.Tracks {
		track := &cue.Tracks[idx]

		trackPCM, sliceErr := apeTrackPCM(pcm, align, totalSamples, rate, timestamps, idx)
		if sliceErr != nil {
			return totalSize, files, fmt.Errorf("ape track %d: %w", track.Number, sliceErr)
		}

		outputPath := filepath.Join(xFile.OutputDir, formatTrackFilename(track, format.ext()))

		size, usedPath, writeErr := writeDecodedAudioFile(
			xFile, outputPath, trackPCM, stream, format, apeTrackTags(cue, track),
		)
		if writeErr != nil {
			return totalSize, files, fmt.Errorf("writing %s track %d: %w", format, track.Number, writeErr)
		}

		totalSize += size

		files = append(files, usedPath)
		xFile.Debugf("Wrote %s track %d: %s (%d bytes)", format, track.Number, usedPath, size)
	}

	return totalSize, files, nil
}

func writeDecodedAudioFile(
	xFile *XFile,
	outputPath string,
	pcm []byte,
	stream ape.Stream,
	format AudioFormat,
	tags map[string]string,
) (uint64, string, error) {
	outFile, usedPath, err := openExtractFile(outputPath, xFile.FileMode)
	if err != nil {
		return 0, "", fmt.Errorf("creating output file: %w", err)
	}

	err = encodeDecodedAudio(xFile.countedWriteSeeker(outFile), pcm, stream, format, tags, xFile.Compression)

	closeErr := outFile.Close()
	if err == nil {
		err = closeErr
	}

	if err != nil {
		_ = os.Remove(usedPath)
		return 0, usedPath, err
	}

	info, err := os.Stat(usedPath)
	if err != nil {
		return 0, usedPath, fmt.Errorf("stat encoded audio: %w", err)
	}

	return uint64(info.Size()), usedPath, nil
}

func encodeDecodedAudio(
	dst io.WriteSeeker,
	pcm []byte,
	stream ape.Stream,
	format AudioFormat,
	tags map[string]string,
	compression int,
) error {
	switch format {
	case AudioFormatWAV:
		return writeWAV(dst, pcm, stream, tags)
	case AudioFormatFLAC:
		return writeFLAC(dst, pcm, stream, tags)
	case AudioFormatAPE:
		return encodeAPE(dst, pcm, stream, tags, compression)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedAPEOutput, format)
	}
}

func encodeAPE(
	dst io.WriteSeeker,
	pcm []byte,
	stream ape.Stream,
	tags map[string]string,
	compression int,
) error {
	err := ape.Encode(dst, pcm, stream, &ape.Options{
		Compression: apeEncodeLevel(compression),
		Tags:        tags,
	})
	if err != nil {
		return fmt.Errorf("encoding ape: %w", err)
	}

	return nil
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
	if track.Performer != "" {
		tags["Artist"] = track.Performer
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

func apeTrackPCM(
	pcm []byte,
	align int,
	totalSamples uint64,
	rate uint32,
	timestamps []cueTimestamp,
	idx int,
) ([]byte, error) {
	start := uint64(0)
	if idx > 0 && idx < len(timestamps) {
		start = timestamps[idx].toSamples(rate)
	}

	end := totalSamples
	if idx+1 < len(timestamps) {
		end = timestamps[idx+1].toSamples(rate)
	}

	if start > end || end > totalSamples {
		return nil, fmt.Errorf("%w: sample range %d:%d of %d", ErrUnsupportedAudio, start, end, totalSamples)
	}

	width := uint64(align)

	return pcm[start*width : end*width], nil
}
