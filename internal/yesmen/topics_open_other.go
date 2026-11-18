//go:build !unix

package yesmen

// topicsOpenFlags: opening a file here does not wait on a writer.
const topicsOpenFlags = 0
