//go:build !unix

package xtractr

func allocatedBytes(string) (int64, bool) {
	return 0, false
}
