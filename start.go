package xtractr

import (
	"os"
	"reflect"
)

// Config is the input data to configure the Xtract queue. Fill this out and
// pass it into NewQueue() to create a queue for archive extractions.
type Config struct {
	BuffSize int         // Size of the extraction channel buffer. Default=1000.
	Parallel int         // Number of concurrent extractions.
	Suffix   string      // The suffix used for temporary folders.
	Logger   Logger      // Logs are sent to this Logger.
	FileMode os.FileMode // Filemode used when writing files.
	DirMode  os.FileMode // Filemode used when writing folders.
}

// Logger allows this library to write logs.
// Use this to capture them in your own flow.
type Logger interface {
	Printf(string, ...interface{})
	Debugf(string, ...interface{})
}

// Xtractr is what you get from NewQueue(). This is the main app struct.
// Use this struct to call Xtractr.Extract() to queue an extraction.
type Xtractr struct {
	*Config
	queue chan *Xtract
}

// NewQueue returns a new Xtractr Queue you can send Xtract jobs into.
// This is where to start if you're creating an extractor queue.
func NewQueue(config *Config) *Xtractr {
	x := parseConfig(config)

	for i := 0; i < x.Parallel; i++ {
		go x.processQueue()
	}

	return x
}

// DefaultBufferSize is the size of the extraction buffer.
// ie. How many jobs can be queued before things get slow.
const DefaultBufferSize = 1000

// parseConfig verifies sane config data and returns the Xtractr struct.
func parseConfig(config *Config) *Xtractr {
	if config.Parallel < 1 {
		config.Parallel = 1
	}

	if config.BuffSize < 1 {
		config.BuffSize = DefaultBufferSize
	}

	if config.Suffix == "" {
		config.Suffix = "_" + reflect.TypeOf(Config{}).PkgPath() // xtractr
	}

	return &Xtractr{
		Config: config,
		queue:  make(chan *Xtract, config.BuffSize),
	}
}

// Stop shuts down the extractor routines. Call this to shut things down. There
// is no re-starting. Once you stop, you need to call NewQueue() to start again.
func (x *Xtractr) Stop() {
	if x.queue == nil {
		return
	}

	close(x.queue)
	x.queue = nil
}

// log writes a log message. Use sparingly and necesarilly.
func (x *Xtractr) log(msg string, v ...interface{}) {
	if x.Logger == nil {
		return
	}

	x.Logger.Printf(msg, v...)
}

// debug writes a debug log message.
func (x *Xtractr) debug(msg string, v ...interface{}) {
	if x.Logger == nil {
		return
	}

	x.Logger.Debugf(msg, v...)
}
