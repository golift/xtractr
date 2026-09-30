package xtractr

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/mewkiz/flac"
	"github.com/mewkiz/flac/frame"
	"github.com/mewkiz/flac/meta"
	"golift.io/ape"
)

const (
	wavFormatPCM   = 1
	wavFormatFloat = 3
	wavFmtChunk    = 16
	wavFmtLen      = 36 // RIFF through the end of the fmt chunk.
	wavDataHeader  = 8
	wavRIFFLead    = 36 // WAVE + fmt chunk + data header, excluding LIST and sample bytes.
	pcm8SignedBias = 128
	bits8          = 8
	bits16         = 16
	bits24         = 24
	signBit24      = 1 << 23
	mask24         = 1<<24 - 1

	flacEncodeBlock = 4096
	flacMaxChannels = 8
	flacMaxRate     = 655350
)

// seekWriter is a WriteSeeker that is not an io.Closer. flac.Encoder.Close
// closes its writer when that writer implements io.Closer, and the extract
// file is closed by the caller after a successful stat.
type seekWriter struct{ io.WriteSeeker }

func writeWAV(dst io.Writer, pcm []byte, stream ape.Stream, tags map[string]string) error {
	info := wavInfoChunk(tags)

	riffSize := uint64(wavRIFFLead) + uint64(len(info)) + uint64(len(pcm))
	if riffSize > maxUint32 {
		return fmt.Errorf("%w: wav exceeds 4 GiB", ErrUnsupportedAPEOutput)
	}

	_, err := dst.Write(wavHeader(stream, uint32(len(pcm)), uint32(len(info))))
	if err != nil {
		return fmt.Errorf("writing wav header: %w", err)
	}

	if len(info) > 0 {
		_, err = dst.Write(info)
		if err != nil {
			return fmt.Errorf("writing wav tags: %w", err)
		}
	}

	dataHeader := make([]byte, wavDataHeader)
	copy(dataHeader, "data")
	binary.LittleEndian.PutUint32(dataHeader[4:], uint32(len(pcm)))

	_, err = dst.Write(dataHeader)
	if err != nil {
		return fmt.Errorf("writing wav data header: %w", err)
	}

	_, err = dst.Write(pcm)
	if err != nil {
		return fmt.Errorf("writing wav audio: %w", err)
	}

	return nil
}

func wavHeader(stream ape.Stream, dataLen, infoLen uint32) []byte {
	format := uint16(wavFormatPCM)
	if stream.Float {
		format = wavFormatFloat
	}

	channels := uint16(stream.Channels)
	bits := uint16(stream.Bits)
	align := channels * bits / bitsPerByte
	rate := uint32(stream.SampleRate)

	header := make([]byte, wavFmtLen)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], wavRIFFLead+infoLen+dataLen)
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], wavFmtChunk)
	binary.LittleEndian.PutUint16(header[20:], format)
	binary.LittleEndian.PutUint16(header[22:], channels)
	binary.LittleEndian.PutUint32(header[24:], rate)
	binary.LittleEndian.PutUint32(header[28:], rate*uint32(align))
	binary.LittleEndian.PutUint16(header[32:], align)
	binary.LittleEndian.PutUint16(header[34:], bits)

	return header
}

func wavInfoChunk(tags map[string]string) []byte {
	if len(tags) == 0 {
		return nil
	}

	fields := []struct {
		key string
		id  string
	}{
		{key: "Artist", id: "IART"},
		{key: "Title", id: "INAM"},
		{key: "Album", id: "IPRD"},
		{key: "Track", id: "ITRK"},
	}

	body := []byte("INFO")

	for _, field := range fields {
		value := tags[field.key]
		if value == "" {
			continue
		}

		payload := append([]byte(value), 0)
		body = binary.LittleEndian.AppendUint32(append(body, field.id...), uint32(len(payload)))
		body = append(body, payload...)

		if len(payload)%2 != 0 {
			body = append(body, 0)
		}
	}

	if len(body) == len("INFO") {
		return nil
	}

	chunk := binary.LittleEndian.AppendUint32([]byte("LIST"), uint32(len(body)))

	return append(chunk, body...)
}

func writeFLAC(dst io.WriteSeeker, pcm []byte, stream ape.Stream, tags map[string]string) error {
	channels, err := flacChannels(pcm, stream)
	if err != nil {
		return err
	}

	samples := len(channels[0])
	info := &meta.StreamInfo{
		BlockSizeMin:  minFLACBlockSize,
		BlockSizeMax:  flacEncodeBlock,
		SampleRate:    uint32(stream.SampleRate),
		NChannels:     uint8(stream.Channels),
		BitsPerSample: uint8(stream.Bits),
		NSamples:      uint64(samples),
	}

	var blocks []*meta.Block
	if comment := apeVorbisComment(tags); comment != nil {
		blocks = append(blocks, comment)
	}

	enc, err := flac.NewEncoder(seekWriter{dst}, info, blocks...)
	if err != nil {
		return fmt.Errorf("creating flac encoder: %w", err)
	}

	err = writeFLACFrames(enc, channels, stream)

	closeErr := enc.Close()
	if err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("encoding flac: %w", err)
	}

	return nil
}

func flacChannels(pcm []byte, stream ape.Stream) ([][]int32, error) {
	err := flacPCMAllowed(stream)
	if err != nil {
		return nil, err
	}

	width := stream.Bits / bitsPerByte
	align := stream.Channels * width

	if align <= 0 || len(pcm)%align != 0 {
		return nil, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	samples := len(pcm) / align
	if samples < minFLACBlockSize {
		return nil, fmt.Errorf("%w (%d samples)", ErrTrackTooShort, samples)
	}

	channels := make([][]int32, stream.Channels)
	for idx := range channels {
		channels[idx] = make([]int32, samples)
	}

	for sample := range samples {
		for idx := range channels {
			off := (sample*stream.Channels + idx) * width
			channels[idx][sample] = pcmSample(pcm[off:off+width], stream.Bits)
		}
	}

	return channels, nil
}

func flacPCMAllowed(stream ape.Stream) error {
	if stream.Float {
		return fmt.Errorf("%w: flac cannot store float pcm", ErrUnsupportedAPEOutput)
	}

	if stream.Channels < 1 || stream.Channels > flacMaxChannels {
		return fmt.Errorf("%w: flac supports 1 to %d channels", ErrUnsupportedAPEOutput, flacMaxChannels)
	}

	switch stream.Bits {
	case bits8, bits16, bits24:
	default:
		return fmt.Errorf("%w: flac supports 8, 16, and 24-bit pcm", ErrUnsupportedAPEOutput)
	}

	if stream.SampleRate <= 0 || stream.SampleRate > flacMaxRate {
		return fmt.Errorf("%w: flac sample rate %d", ErrUnsupportedAPEOutput, stream.SampleRate)
	}

	return nil
}

func pcmSample(raw []byte, bits int) int32 {
	switch bits {
	case bits8:
		return int32(raw[0]) - pcm8SignedBias
	case bits16:
		return int32(int16(binary.LittleEndian.Uint16(raw)))
	default:
		value := int32(raw[0]) | int32(raw[1])<<bits8 | int32(raw[2])<<bits16
		if value&signBit24 != 0 {
			value |= ^mask24
		}

		return value
	}
}

func writeFLACFrames(enc *flac.Encoder, channels [][]int32, stream ape.Stream) error {
	total := len(channels[0])
	pos := 0

	for pos < total {
		count := flacEncodeBlock
		if remain := total - pos; count > remain {
			count = remain
		}

		if rest := total - (pos + count); rest > 0 && rest < minFLACBlockSize {
			count = total - pos - minFLACBlockSize
		}

		err := enc.WriteFrame(flacPCMFrame(channels, pos, count, stream))
		if err != nil {
			return fmt.Errorf("writing flac frame: %w", err)
		}

		pos += count
	}

	return nil
}

func flacPCMFrame(channels [][]int32, pos, count int, stream ape.Stream) *frame.Frame {
	subs := make([]*frame.Subframe, len(channels))

	for idx, samples := range channels {
		clip := make([]int32, count)
		copy(clip, samples[pos:pos+count])

		subs[idx] = &frame.Subframe{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
			Samples:   clip,
			NSamples:  count,
		}
	}

	return &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: false,
			BlockSize:         uint16(count),
			SampleRate:        uint32(stream.SampleRate),
			Channels:          frame.Channels(stream.Channels - 1),
			BitsPerSample:     uint8(stream.Bits),
		},
		Subframes: subs,
	}
}

func apeVorbisComment(tags map[string]string) *meta.Block {
	if len(tags) == 0 {
		return nil
	}

	pairs := make([][2]string, 0, len(tags))

	if tags["Title"] != "" {
		pairs = append(pairs, [2]string{"TITLE", tags["Title"]})
	}

	if tags["Track"] != "" {
		pairs = append(pairs, [2]string{"TRACKNUMBER", tags["Track"]})
	}

	if tags["Album"] != "" {
		pairs = append(pairs, [2]string{"ALBUM", tags["Album"]})
	}

	if tags["Artist"] != "" {
		pairs = append(pairs, [2]string{"ARTIST", tags["Artist"]})
	}

	if len(pairs) == 0 {
		return nil
	}

	return &meta.Block{
		Header: meta.Header{Type: meta.TypeVorbisComment, Length: 1},
		Body: &meta.VorbisComment{
			Vendor: "golift.io/xtractr",
			Tags:   pairs,
		},
	}
}
