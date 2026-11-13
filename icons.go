package main

import _ "embed"

// The application and tray share the yak outline from resources/icon.svg.
// MyGo packages icon.png as the platform's application icon; these embedded
// copies also work with go run and builds outside an application bundle.

//go:embed resources/icon.png
var appIcon []byte

// trayIcon is black on transparent. MyGo tints the template to match the
// menu bar or taskbar on macOS, Windows and Linux.
//
//go:embed resources/tray.png
var trayIcon []byte
