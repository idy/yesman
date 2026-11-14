package main

import (
	"context"
	"github.com/idy/yesman/internal/nativepermissions"
)

func (a *app) permissionManager() *nativepermissions.Manager {
	a.grantsOnce.Do(func() { a.grants = nativepermissions.New() })
	return a.grants
}

// Grants reads OS authorization only. It never prompts or probes user files.
func (s *service) Grants(ctx context.Context) nativepermissions.View {
	return s.app.permissionManager().List(ctx)
}

// GrantAction accepts only a known permission and request/settings action.
func (s *service) GrantAction(ctx context.Context, id nativepermissions.ID, action string) (nativepermissions.Result, error) {
	return s.app.permissionManager().Act(ctx, id, action)
}
