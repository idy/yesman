//go:build !darwin || !cgo

package nativepermissions

import (
	"context"
	"errors"
	"runtime"

	"github.com/egoist/mygo"
)

type systemAdapter struct{}

func newAdapter() adapter { return systemAdapter{} }
func (systemAdapter) query(_ context.Context, id ID) (Item, error) {
	return systemItem(runtime.GOOS, id), nil
}
func (systemAdapter) request(context.Context, ID) error {
	return errors.New("This platform has no application-wide permission request for this item")
}
func (systemAdapter) open(ctx context.Context, id ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	url := settingsURL(runtime.GOOS, id)
	if url == "" {
		return errors.New("No system settings entry is available for this permission")
	}
	return mygo.Shell.OpenExternal(url)
}
func (systemAdapter) identity() *App { return nil }
