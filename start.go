package xtractr

import (
	"errors"
	"os"
)

// Sane defaults.
const (
	DefaultDirMode  = 0o755
	DefaultFileMode = 0o644
	DefaultSuffix   = "_xtractr"
	// DefaultBufferSize is the size of the extraction buffer.
	// ie. How many jobs can be queued before things get slow.
	DefaultBufferSize = 1000
)

// Config is the input data to configure the Xtract queue. Fill this out and
// pass it into NewQueue() to create a queue for archive extractions.
type Config struct {
	// Size of the extraction channel buffer. Default=1000.
	// Use -1 for unbuffered channel. Not recommend.
	BuffSize int
	// Number of concurrent extractions allowed.
	Parallel int
	// Filemode used when writing files, tar ignores this, so does Windows.
	FileMode os.FileMode
	// Filemode used when writing folders, tar ignores this.
	DirMode os.FileMode
	// The suffix used for temporary folders.
	Suffix string
	// Logs are sent to this Logger.
	Logger
}

// Logger allows this library to write logs.
// Use this to capture them in your own flow.
type Logger interface {
	Printf(format string, v ...interface{})
	Debugf(format string, v ...interface{})
}

// Xtractr is what you get from NewQueue(). This is the main app struct.
// Use this struct to call Xtractr.Extract() to queue an extraction.
type Xtractr struct {
	config *Config
	queue  chan *Xtract
	done   chan struct{}
}

// Custom errors returned by this module.
var (
	ErrQueueStopped       = errors.New("extractor queue stopped, cannot extract")
	ErrNoCompressedFiles  = errors.New("no compressed files found")
	ErrUnknownArchiveType = errors.New("unknown archive file type")
	ErrInvalidPath        = errors.New("archived file contains invalid path")
	ErrInvalidHead        = errors.New("archived file contains invalid header file")
	ErrQueueRunning       = errors.New("extractor queue running, cannot start")
	ErrNoConfig           = errors.New("call NewQueue() to initialize a queue")
	ErrNoLogger           = errors.New("xtractr.Config.Logger must be non-nil")
)

// NewQueue returns a new Xtractr Queue you can send Xtract jobs into.
// This is where to start if you're creating an extractor queue.
// You must provide a Logger in the config, everything else is optional.
func NewQueue(config *Config) *Xtractr {
	x := parseConfig(config)
	if err := x.Start(); err != nil {
		panic(err)
	}

	return x
}

// Start restarts the queue. This can be called only after you call Stop().
func (x *Xtractr) Start() error {
	if x.queue != nil {
		// This happens if you call Start() without calling Stop() first.
		return ErrQueueRunning
	}

	if x.config == nil {
		// This happens if you call Start() on an *Xtractr without NewQueue().
		return ErrNoConfig
	}

	if x.config.Logger == nil {
		// This happens if you forget a *Logger.
		return ErrNoLogger
	}

	x.queue = make(chan *Xtract, x.config.BuffSize)

	for i := 0; i < x.config.Parallel; i++ {
		go x.processQueue()
	}

	return nil
}

// parseConfig verifies sane config data and returns the Xtractr struct.
func parseConfig(config *Config) *Xtractr {
	if config.FileMode == 0 {
		config.FileMode = DefaultFileMode
	}

	if config.DirMode == 0 {
		config.DirMode = DefaultDirMode
	}

	if config.Parallel < 1 {
		config.Parallel = 1
	}

	if config.BuffSize == 0 {
		config.BuffSize = DefaultBufferSize
	} else if config.BuffSize < 0 {
		config.BuffSize = 0
	}

	if config.Suffix == "" {
		config.Suffix = DefaultSuffix
	}

	return &Xtractr{
		config: config,
		done:   make(chan struct{}),
	}
}

// Stop shuts down the extractor routines. Call this to shut things down.
func (x *Xtractr) Stop() {
	if x.queue == nil {
		return
	}

	close(x.queue)

	// Wait until all running extractions are done.
	for i := 0; i < x.config.Parallel; i++ {
		<-x.done
	}

	x.queue = nil
}
