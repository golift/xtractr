package extractorr

import (
	"io/ioutil"
	"log"
)

// DefaultSuffix is used if no suffix is provided.
// The suffix is used for temporary folders and files.
const DefaultSuffix = "_extractorr"

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

// Extractorr is what you get from New(). This is the main app struct.
type Extractorr struct {
	Config *Config
	queue  chan *Extract
}

// New returns a new extractorr you can send jobs into.
func New(config *Config) *Extractorr {
	e := parseConfig(config)

	for i := 0; i < e.Config.Parallel; i++ {
		go e.processQueue()
	}

	return e
}

// parseConfig verifies sane config data and returns the Extractorr struct.
func parseConfig(config *Config) *Extractorr {
	if config.Parallel < 1 {
		config.Parallel = 1
	}

	if config.Suffix == "" {
		config.Suffix = DefaultSuffix
	}

	if config.Logger == nil {
		config.Logger = log.New(ioutil.Discard, "", 0)
	}

	return &Extractorr{
		Config: config,
		queue:  make(chan *Extract, BufferSize),
	}
}

// Stop shuts down the extractor routines.
func (e Extractorr) Stop() {
	if e.queue == nil {
		return
	}

	close(e.queue)
	e.queue = nil
}

// log writes a log message.
func (e Extractorr) log(msg string, v ...interface{}) {
	e.Config.Logger.Printf(msg, v...)
}

// debug writes a debug log message.
func (e Extractorr) debug(msg string, v ...interface{}) {
	if e.Config.Debug {
		e.Config.Logger.Printf("[DEBUG] "+msg, v...)
	}
}
