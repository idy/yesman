//go:build (!darwin && !windows && !linux) || (darwin && !cgo)

package chime

func play([]byte) error {
	return ErrUnavailable
}
