package xtractr

import "fmt"

/* This file will surely grow when someone writes a proper character encoding detector. */

// EncoderInput is used as input for a custom encoder procedure.
type EncoderInput struct {
	FileName string
	XFile    *XFile
}

// decode a string using the provided decoder.
func (x *XFile) decode(input string) (string, error) {
	if x.Encoder == nil {
		return input, nil
	}

	encoding := x.Encoder(&EncoderInput{FileName: input, XFile: x})
	if encoding == nil {
		return input, nil
	}

	output, err := encoding.String(input)
	if err != nil {
		return "", fmt.Errorf("decoding file name: %w", err)
	}

	return output, nil
}
