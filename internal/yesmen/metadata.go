package yesmen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxYesmanMetadataBytes = 1 << 20

var errYesmanMetadataTooLarge = errors.New("yesman metadata exceeds 1 MiB; reduce its encoded size before retrying")

// MetadataError identifies a workspace whose observed metadata exceeds the
// read/write budget. It is a diagnostic, never a workspace identity.
type MetadataError struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Limit int64  `json:"limit"`
}

func (e *MetadataError) Error() string {
	return fmt.Sprintf("%s: observed metadata size %d bytes exceeds 1 MiB (%d bytes); back up the file and reduce its encoded size before retrying, preserving hiredAt", e.Path, e.Size, e.Limit)
}

func (e *MetadataError) Unwrap() error { return errYesmanMetadataTooLarge }

// MetadataErrors accompanies the healthy workspaces returned by List.
// Callers can report these unavailable workspaces without admitting them to
// workspace/agent membership or discarding the healthy results.
type MetadataErrors []MetadataError

func (es MetadataErrors) Error() string {
	messages := make([]string, len(es))
	for i := range es {
		messages[i] = es[i].Error()
	}
	return strings.Join(messages, "\n")
}

func (es MetadataErrors) Unwrap() error { return errYesmanMetadataTooLarge }

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
		return &MetadataError{Path: path, Size: info.Size(), Limit: maxYesmanMetadataBytes}
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
		return &MetadataError{Path: path, Size: info.Size(), Limit: maxYesmanMetadataBytes}
	}
	// A regular file can grow after Stat. Bound the actual read as well.
	data, err := io.ReadAll(io.LimitReader(f, maxYesmanMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxYesmanMetadataBytes {
		size := int64(len(data))
		if latest, err := f.Stat(); err == nil {
			size = max(size, latest.Size())
		}
		return &MetadataError{Path: path, Size: size, Limit: maxYesmanMetadataBytes}
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
	data = append(data, '\n')
	if len(data) > maxYesmanMetadataBytes {
		return &MetadataError{Path: path, Size: int64(len(data)), Limit: maxYesmanMetadataBytes}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
