//go:build !darwin

package xtractr

import "os"

// punchFileHole is a no-op where a seek over zeros is already a hole
// (Linux, FreeBSD) or where the file was marked sparse before the seek
// (Windows).
func punchFileHole(*os.File, int64, int64) {}
