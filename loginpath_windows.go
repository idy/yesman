//go:build windows

package main

// importLoginPath does nothing on Windows, where apps get the user's PATH.
func importLoginPath() {}
