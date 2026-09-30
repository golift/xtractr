package xtractr

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

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
	pad := len(pcm) & 1

	riffSize := uint64(wavRIFFLead) + uint64(len(info)) + uint64(len(pcm)) + uint64(pad)
	if riffSize > maxUint32 {
		return fmt.Errorf("%w: wav exceeds 4 GiB", ErrUnsupportedAPEOutput)
	}

	header, err := wavHeader(stream, uint32(len(pcm)), uint32(len(info)))
	if err != nil {
		return err
	}

	_, err = dst.Write(header)
	if err != nil {
		return fmt.Errorf("writing wav header: %w", err)
	}

	if len(info) > 0 {
		_, err = dst.Write(info)
		if err != nil {
			return fmt.Errorf("writing wav tags: %w", err)
		}
	}

	err = writeWAVData(dst, pcm)
	if err != nil {
		return err
	}

	if pad == 0 {
		return nil
	}

	_, err = dst.Write([]byte{0})
	if err != nil {
		return fmt.Errorf("writing wav pad: %w", err)
	}

	return nil
}

func writeWAVData(dst io.Writer, pcm []byte) error {
	dataHeader := make([]byte, wavDataHeader)
	copy(dataHeader, "data")
	binary.LittleEndian.PutUint32(dataHeader[4:], uint32(len(pcm)))

	_, err := dst.Write(dataHeader)
	if err != nil {
		return fmt.Errorf("writing wav data header: %w", err)
	}

	_, err = dst.Write(pcm)
	if err != nil {
		return fmt.Errorf("writing wav audio: %w", err)
	}

	return nil
}

func wavHeader(stream ape.Stream, dataLen, infoLen uint32) ([]byte, error) {
	format := uint16(wavFormatPCM)
	if stream.Float {
		format = wavFormatFloat
	}

	align, byteRate, err := wavLayout(stream)
	if err != nil {
		return nil, err
	}

	header := make([]byte, wavFmtLen)
	copy(header[0:], "RIFF")
	// Odd chunks are padded to a word. The pad is in the RIFF size and not in the data size.
	binary.LittleEndian.PutUint32(header[4:], wavRIFFLead+infoLen+dataLen+(dataLen&1))
	copy(header[8:], "WAVE")
	copy(header[12:], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], wavFmtChunk)
	binary.LittleEndian.PutUint16(header[20:], format)
	binary.LittleEndian.PutUint16(header[22:], uint16(stream.Channels))
	binary.LittleEndian.PutUint32(header[24:], uint32(stream.SampleRate))
	binary.LittleEndian.PutUint32(header[28:], byteRate)
	binary.LittleEndian.PutUint16(header[32:], align)
	binary.LittleEndian.PutUint16(header[34:], uint16(stream.Bits))

	return header, nil
}

// wavLayout is the fmt block-align and byte-rate fields. Both are fixed-width,
// so a rate the APE decoder accepts can still be too wide for WAV.
func wavLayout(stream ape.Stream) (uint16, uint32, error) {
	if !wavStreamFits(stream) {
		return 0, 0, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	align := stream.Channels * stream.Bits / bitsPerByte
	byteRate := uint64(stream.SampleRate) * uint64(align)

	if align <= 0 || align > math.MaxUint16 || byteRate > maxUint32 {
		return 0, 0, fmt.Errorf("%w: wav byte rate", ErrUnsupportedAPEOutput)
	}

	return uint16(align), uint32(byteRate), nil
}

func wavStreamFits(stream ape.Stream) bool {
	if stream.Channels <= 0 || stream.Channels > math.MaxUint16 || stream.Bits <= 0 || stream.Bits > math.MaxUint16 {
		return false
	}

	if stream.Bits%bitsPerByte != 0 || stream.SampleRate <= 0 || uint64(stream.SampleRate) > maxUint32 {
		return false
	}

	return true
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

// pcmSink writes one decoded track. write receives interleaved PCM.
// finish flushes the container.
type pcmSink interface {
	write(pcm []byte) error
	finish() error
}

type wavSink struct {
	dst  io.Writer
	left int
	pad  bool
}

func newWAVSink(dst io.Writer, stream ape.Stream, tags map[string]string, samples int) (*wavSink, error) {
	align := stream.Channels * stream.Bits / bitsPerByte
	if align <= 0 || samples <= 0 {
		return nil, fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	dataLen := uint64(samples) * uint64(align)
	info := wavInfoChunk(tags)
	riffSize := uint64(wavRIFFLead) + uint64(len(info)) + dataLen + (dataLen & 1)

	if dataLen > maxUint32 || riffSize > maxUint32 {
		return nil, fmt.Errorf("%w: wav exceeds 4 GiB", ErrUnsupportedAPEOutput)
	}

	header, err := wavHeader(stream, uint32(dataLen), uint32(len(info)))
	if err != nil {
		return nil, err
	}

	_, err = dst.Write(header)
	if err != nil {
		return nil, fmt.Errorf("writing wav header: %w", err)
	}

	if len(info) > 0 {
		_, err = dst.Write(info)
		if err != nil {
			return nil, fmt.Errorf("writing wav tags: %w", err)
		}
	}

	err = writeDataChunkHeader(dst, uint32(dataLen))
	if err != nil {
		return nil, err
	}

	return &wavSink{dst: dst, left: int(dataLen), pad: dataLen&1 == 1}, nil
}

func (w *wavSink) write(pcm []byte) error {
	if len(pcm) > w.left {
		return fmt.Errorf("%w: wav longer than header", ErrUnsupportedAudio)
	}

	_, err := w.dst.Write(pcm)
	if err != nil {
		return fmt.Errorf("writing wav audio: %w", err)
	}

	w.left -= len(pcm)

	return nil
}

func (w *wavSink) finish() error {
	if w.left != 0 {
		return fmt.Errorf("%w: short wav", ErrUnsupportedAudio)
	}

	if !w.pad {
		return nil
	}

	_, err := w.dst.Write([]byte{0})
	if err != nil {
		return fmt.Errorf("writing wav pad: %w", err)
	}

	return nil
}

func writeDataChunkHeader(dst io.Writer, dataLen uint32) error {
	dataHeader := make([]byte, wavDataHeader)
	copy(dataHeader, "data")
	binary.LittleEndian.PutUint32(dataHeader[4:], dataLen)

	_, err := dst.Write(dataHeader)
	if err != nil {
		return fmt.Errorf("writing wav data header: %w", err)
	}

	return nil
}

type flacSink struct {
	enc     *flac.Encoder
	stream  ape.Stream
	raw     []byte
	align   int
	total   int
	written int
}

func newFLACSink(dst io.WriteSeeker, stream ape.Stream, tags map[string]string, samples int) (*flacSink, error) {
	err := flacPCMAllowed(stream)
	if err != nil {
		return nil, err
	}

	if samples < minFLACBlockSize {
		return nil, fmt.Errorf("%w (%d samples)", ErrTrackTooShort, samples)
	}

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
		return nil, fmt.Errorf("creating flac encoder: %w", err)
	}

	return &flacSink{
		enc:    enc,
		stream: stream,
		align:  stream.Channels * stream.Bits / bitsPerByte,
		total:  samples,
	}, nil
}

func (s *flacSink) write(pcm []byte) error {
	if len(pcm)%s.align != 0 {
		return fmt.Errorf("%w: decoded ape pcm", ErrUnsupportedAudio)
	}

	s.raw = append(s.raw, pcm...)

	return s.emit(false)
}

func (s *flacSink) finish() error {
	err := s.emit(true)

	closeErr := s.enc.Close()
	if err == nil {
		err = closeErr
	}

	if err != nil {
		return fmt.Errorf("encoding flac: %w", err)
	}

	return nil
}

func (s *flacSink) emit(final bool) error {
	for {
		count, wait, err := s.nextBlock(final)
		if err != nil || wait {
			return err
		}

		err = s.enc.WriteFrame(flacFrameFromPCM(s.raw, s.stream, count))
		if err != nil {
			return fmt.Errorf("writing flac frame: %w", err)
		}

		s.written += count
		s.raw = s.raw[count*s.align:]
	}
}

func (s *flacSink) nextBlock(final bool) (count int, wait bool, err error) {
	available := len(s.raw) / s.align
	remain := s.total - s.written

	if available == 0 || remain == 0 {
		return 0, true, nil
	}

	count = min(flacEncodeBlock, remain)
	if tail := remain - count; tail > 0 && tail < minFLACBlockSize {
		count = remain - minFLACBlockSize
	}

	if count < minFLACBlockSize {
		return 0, false, fmt.Errorf("%w (%d samples)", ErrTrackTooShort, s.total)
	}

	if count > available {
		if !final {
			return 0, true, nil
		}

		return 0, false, fmt.Errorf("%w: truncated flac pcm", ErrUnsupportedAudio)
	}

	return count, false, nil
}

func flacFrameFromPCM(pcm []byte, stream ape.Stream, count int) *frame.Frame {
	width := stream.Bits / bitsPerByte
	subs := make([]*frame.Subframe, stream.Channels)

	for channel := range stream.Channels {
		samples := make([]int32, count)

		for idx := range count {
			off := (idx*stream.Channels + channel) * width
			samples[idx] = pcmSample(pcm[off:off+width], stream.Bits)
		}

		subs[channel] = &frame.Subframe{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
			Samples:   samples,
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

type apeSink struct {
	enc *ape.Encoder
}

func newAPESink(
	dst io.WriteSeeker,
	stream ape.Stream,
	tags map[string]string,
	samples, compression int,
) (*apeSink, error) {
	enc, err := ape.NewEncoder(dst, stream, samples, &ape.Options{
		Compression: apeEncodeLevel(compression),
		Tags:        tags,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding ape: %w", err)
	}

	return &apeSink{enc: enc}, nil
}

func (s *apeSink) write(pcm []byte) error {
	err := s.enc.Write(pcm)
	if err != nil {
		return fmt.Errorf("encoding ape: %w", err)
	}

	return nil
}

func (s *apeSink) finish() error {
	err := s.enc.Close()
	if err != nil {
		return fmt.Errorf("encoding ape: %w", err)
	}

	return nil
}
