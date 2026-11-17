//go:build !unix

package yesmen

import "os"

// These platforms have no Unix FIFO open flag. The opened file is still
// checked for regular type and bounded before any read.
const metadataOpenFlags = os.O_RDONLY
