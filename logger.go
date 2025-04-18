package xtractr

// NoLogger gives you an empty Logger for cases when you don't want any output.
func NoLogger() Logger { return &antiLogger{} }

type antiLogger struct{}

func (*antiLogger) Printf(_ string, _ ...any) {}
func (*antiLogger) Debugf(_ string, _ ...any) {}
