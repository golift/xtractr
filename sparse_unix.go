//go:build !windows

package xtractr

import "os"

// markFileSparse is a no-op off Windows. A seek over zeros becomes a hole
// there without a flag. NTFS fills that gap unless the file is marked sparse.
func markFileSparse(*os.File) error {
	return nil
}
