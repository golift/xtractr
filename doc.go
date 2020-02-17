// Package xtractr provides methods and procedures to extract compressed
// archive files. It can be used in two ways. The simplest method is to pass
// an archive file path and an output path to ExtractFile(). This decompresses
// the provided file and returns some information about the data written.
//
// The other, more sophisticated way to extract files is with a queue.
// The queue method allows you to send an Xtract into a channel where it's
// queued up and extracted in order. The number of concurrent extractions is
// configured when the queue is created. A provided callback method is run
// when a queued Xtract begins and it's run again when the Xtract finishes.
package xtractr
