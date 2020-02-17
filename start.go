package xtractr

import (
	"io/ioutil"
	"log"
)

// DefaultSuffix is used if no suffix is provided.
// The suffix is used for temporary folders and files.
const DefaultSuffix = "_xtractr"

// BufferSize is the size of the extraction buffer.
// ie. How many jobs can be queued before things get slow.
const BufferSize = 1000

// ExtType represents a supported compression scheme.
type ExtType string

// Config is the input data to confiure this library for use.
type Config struct {
	Parallel int
	ExtTypes []ExtType
	Logger   *log.Logger
	Debug    bool
	Suffix   string
}

// Xtractr is what you get from New(). This is the main app struct.
type Xtractr struct {
	*Config
	queue chan *Xtract
}

// NewQueue returns a new Xtractr Queue you can send jobs into.
func NewQueue(config *Config) *Xtractr {
	x := parseConfig(config)

	for i := 0; i < x.Parallel; i++ {
		go x.processQueue()
	}

	return x
}

// parseConfig verifies sane config data and returns the Xtractr struct.
func parseConfig(config *Config) *Xtractr {
	if config.Parallel < 1 {
		config.Parallel = 1
	}

	if config.Suffix == "" {
		config.Suffix = DefaultSuffix
	}

	if config.Logger == nil {
		config.Logger = log.New(ioutil.Discard, "", 0)
	}

	return &Xtractr{
		Config: config,
		queue:  make(chan *Xtract, BufferSize),
	}
}

// Stop shuts down the extractor routines.
func (x *Xtractr) Stop() {
	if x.queue == nil {
		return
	}

	close(x.queue)
	x.queue = nil
}

// log writes a log message.
func (x *Xtractr) log(msg string, v ...interface{}) {
	x.Logger.Printf(msg, v...)
}

// debug writes a debug log message.
func (x *Xtractr) debug(msg string, v ...interface{}) {
	if x.Debug {
		x.Logger.Printf("[DEBUG] "+msg, v...)
	}
}
