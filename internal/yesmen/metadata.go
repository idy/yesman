package yesmen

import (
	"encoding/json"
	"errors"
	"io"
	"os"
)

const maxYesmanMetadataBytes = 1 << 20

// readYesmanMetadata follows existing metadata aliases, but validates the
// opened file rather than a pathname that could change before open. Callers
// hold metaMu through close to coordinate atomic replacement on Windows.
func readYesmanMetadata(path string, v any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxYesmanMetadataBytes {
		return errors.New("yesman metadata must be a regular file of at most 1 MiB")
	}
	f, err := os.OpenFile(path, metadataOpenFlags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxYesmanMetadataBytes {
		return errors.New("yesman metadata must be a regular file of at most 1 MiB")
	}
	// A regular file can grow after Stat. Bound the actual read as well.
	data, err := io.ReadAll(io.LimitReader(f, maxYesmanMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxYesmanMetadataBytes {
		return errors.New("yesman metadata exceeds 1 MiB")
	}
	return json.Unmarshal(data, v)
}
