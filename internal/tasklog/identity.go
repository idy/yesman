package tasklog

import (
	"errors"
	"os"
)

// Identity is captured from an open descriptor, never re-resolved from a
// pathname. Birth/creation time protects against later inode/FileID reuse.
type Identity struct {
	Platform string `json:"platform"`
	Volume   string `json:"volume"`
	ID       string `json:"id"`
	Birth    string `json:"birth"`
}

func (i Identity) valid() bool {
	return i.Platform != "" && i.Volume != "" && i.ID != "" && i.Birth != "" &&
		(i.Platform != "darwin" || i.Birth != "0:0")
}
func requireIdentity(f *os.File, expected Identity) error {
	actual, err := fileIdentity(f)
	if err != nil {
		return err
	}
	if !expected.valid() || actual != expected {
		return errors.New("prepared source identity changed; preserve both generations and reconcile offline")
	}
	return nil
}
