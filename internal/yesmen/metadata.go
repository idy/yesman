package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxYesmanMetadataBytes = 1 << 20

var errYesmanMetadataTooLarge = errors.New("yesman metadata exceeds 1 MiB; reduce its encoded size before retrying")

// readYesmanMetadata follows existing metadata aliases, but validates the
// opened file rather than a pathname that could change before open. Callers
// hold metaMu through close to coordinate atomic replacement on Windows.
func readYesmanMetadata(path string, v any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("yesman metadata must be a regular file")
	}
	if info.Size() > maxYesmanMetadataBytes {
		return fmt.Errorf("%s: %w", path, errYesmanMetadataTooLarge)
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
	if !info.Mode().IsRegular() {
		return errors.New("yesman metadata must be a regular file")
	}
	if info.Size() > maxYesmanMetadataBytes {
		return fmt.Errorf("%s: %w", path, errYesmanMetadataTooLarge)
	}
	// A regular file can grow after Stat. Bound the actual read as well.
	data, err := io.ReadAll(io.LimitReader(f, maxYesmanMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxYesmanMetadataBytes {
		return fmt.Errorf("%s: %w", path, errYesmanMetadataTooLarge)
	}
	return json.Unmarshal(data, v)
}

// writeYesmanMetadata checks the final encoding (including the newline)
// before touching either file. Successful writes must round-trip through the
// metadata reader, even when JSON escaping or indentation expands the input.
func writeYesmanMetadata(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(data)+1 > maxYesmanMetadataBytes {
		return fmt.Errorf("%s: encoded size %d bytes: %w", path, len(data)+1, errYesmanMetadataTooLarge)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
