//go:build linux || darwin

// Package fsutil holds the file system operations of capze that the standard
// library does not offer: directories that are worked on through their file
// descriptors rather than their paths, a lock that other capze processes
// honour, a recursive removal that never crosses a mount point, and flushing
// a file system to disk.
package fsutil
