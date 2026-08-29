package xtractr

// rememberFile returns false when path is a later alias of a file already in
// seen (same device+inode, or resolved path when inode data is unavailable).
// Paths that cannot be identified are kept (fail open).
func rememberFile(seen map[string]struct{}, path string) bool {
	identity, ok := identifyFile(path)
	if !ok {
		return true
	}

	if _, dup := seen[identity]; dup {
		return false
	}

	seen[identity] = struct{}{}

	return true
}
