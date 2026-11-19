package schedule

import (
	"errors"
	"os"

	"github.com/idy/yesman/internal/filelock"
)

var errLocked = errors.New("another yesman app holds the scheduled tasks")

func lockFile(path string) (*os.File, error) {
	f, err := filelock.Try(path)
	if errors.Is(err, filelock.ErrLocked) {
		return nil, errLocked
	}
	return f, err
}
