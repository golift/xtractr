package xtractr

import (
	"errors"
	"strings"
	"syscall"
)

// Package-level errors for extraction and queue operations.
var (
	ErrNameTooLong = errors.New("could not find available truncated path after 999 attempts")

	// Queue / start.

	ErrQueueStopped       = errors.New("extractor queue stopped, cannot extract")
	ErrNoCompressedFiles  = errors.New("no compressed files found")
	ErrUnknownArchiveType = errors.New("unknown archive file type")
	ErrInvalidPath        = errors.New("archived file contains invalid path")
	ErrInvalidHead        = errors.New("archived file contains invalid header file")
	ErrQueueRunning       = errors.New("extractor queue running, cannot start")
	ErrNoConfig           = errors.New("call NewQueue() to initialize a queue")
	ErrNoLogger           = errors.New("xtractr.Config.Logger must be non-nil")

	// CUE sheet.

	ErrNoCueFile        = errors.New("cue sheet does not reference a FILE")
	ErrNoTracks         = errors.New("cue sheet contains no tracks")
	ErrAudioNotFound    = errors.New("audio file referenced by cue sheet not found")
	ErrUnsupportedAudio = errors.New("cue sheet references unsupported audio format (only FLAC is supported)")

	// RPM.

	ErrUnsupportedRPMCompression = errors.New("unsupported rpm compression")
	ErrUnsupportedRPMArchiveFmt  = errors.New("unsupported rpm archive format")
)

// ExtractError is a rich error type that can carry multiple errors and warnings
// from an extraction attempt. Consumers can use errors.As to retrieve it.
type ExtractError struct {
	// Errs holds all errors encountered during extraction attempts.
	Errs []error
	// Warnings holds non-fatal messages such as extension mismatches or truncated names.
	Warnings []string
	// FilePath is the path to the archive that failed to extract.
	FilePath string
	// OutputDir is the directory where extraction was attempted.
	OutputDir string
	// BytesWritten is the number of bytes written before the error (partial progress).
	BytesWritten uint64
	// ArchiveType is the detected or expected archive type (e.g. "zip", "tar.gz", "7z").
	ArchiveType string
}

// NewExtractError wraps a single error as an ExtractError with optional context.
// filePath and outputDir are the archive path and extraction output directory;
// archiveType is e.g. "zip", "tar.gz". Pass empty strings for unknown.
func NewExtractError(err error, filePath, outputDir string, bytesWritten uint64, archiveType string) *ExtractError {
	if err == nil {
		return nil
	}

	return &ExtractError{
		Errs:         []error{err},
		FilePath:     filePath,
		OutputDir:    outputDir,
		BytesWritten: bytesWritten,
		ArchiveType:  archiveType,
	}
}

// Error satisfies the error interface. It returns a combined message from all errors.
func (e *ExtractError) Error() string {
	msgs := strings.Builder{}
	for _, err := range e.Errs {
		if msgs.Len() > 0 {
			msgs.WriteString("; ")
		}

		msgs.WriteString(err.Error())
	}

	msg := "extraction failed: " + msgs.String()
	if e.FilePath != "" {
		msg += " (file: " + e.FilePath + ")"
	}

	return msg
}

// Unwrap returns the list of wrapped errors for use with errors.Is and errors.As.
func (e *ExtractError) Unwrap() []error {
	return e.Errs
}

// HasWarnings returns true if any non-fatal warnings were collected.
func (e *ExtractError) HasWarnings() bool {
	return len(e.Warnings) > 0
}

// WrapExtractError ensures the error is an ExtractError with context from xFile.
// If err is already an *ExtractError, its context fields are filled from xFile when empty.
// If err is nil, returns nil. xFile may be nil when only path/context are available.
func WrapExtractError(err error, xFile *XFile, bytesWritten uint64, archiveType string) error {
	if err == nil {
		return nil
	}

	var extErr *ExtractError
	if !errors.As(err, &extErr) {
		filePath := ""
		outputDir := ""

		if xFile != nil {
			filePath = xFile.FilePath
			outputDir = xFile.OutputDir
		}

		return NewExtractError(err, filePath, outputDir, bytesWritten, archiveType)
	}

	if xFile != nil {
		if extErr.FilePath == "" {
			extErr.FilePath = xFile.FilePath
		}

		if extErr.OutputDir == "" {
			extErr.OutputDir = xFile.OutputDir
		}
	}

	if extErr.BytesWritten == 0 {
		extErr.BytesWritten = bytesWritten
	}

	if extErr.ArchiveType == "" {
		extErr.ArchiveType = archiveType
	}

	return extErr
}

// IsErrNameTooLong reports whether err indicates a "file name too long" condition.
// On Unix this corresponds to syscall.ENAMETOOLONG; it also matches the
// "file name too long" error message so it works on all platforms (e.g. Windows).
func IsErrNameTooLong(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, syscall.ENAMETOOLONG) {
		return true
	}

	return strings.Contains(err.Error(), "file name too long")
}
