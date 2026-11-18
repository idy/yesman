package nativepermissions

import (
	"strings"
	"syscall"
)

// osVersion is the macOS product version, such as "15.4.1". System Settings
// names some privacy lists differently by version; an empty value is unknown.
func osVersion() string {
	version, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(version)
}
