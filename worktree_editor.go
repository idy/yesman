package main

import (
	"context"
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/idy/yesman/internal/yesmen"
)

// Before enabling a text editor, hold its top-level directory even if Files
// opened it from All files before receiving the worktree inventory.
func (s *service) BeginWorktreeEdit(ctx context.Context, name, file string, since time.Time, checkout *FileCheckout) (string, error) {
	dir, rel, nested := strings.Cut(cleanRel(file), "/")
	if !nested || rel == "" || dir == ".yesman" {
		return "", nil
	}
	if !filepath.IsLocal(dir) || filepath.Base(dir) != dir || dir == "." {
		return "", errors.New("invalid file directory")
	}
	var repo, id string
	if checkout != nil {
		if checkout.Dir != dir || !checkout.Hire.Equal(since) || checkout.ID == "" {
			return "", errors.New("file is outside this checkout")
		}
		repo, id = checkout.Repo, checkout.ID
	}
	a, win := s.app, mygo.CallerWindow(ctx)
	a.wmu.Lock()
	epoch := a.editorEpoch
	a.wmu.Unlock()
	var release func()
	err := a.yesmen.Use(name, func(y yesmen.Yesman) error {
		if !y.HiredAt.Equal(since) {
			return yesmen.ErrRehired
		}
		var err error
		release, err = a.pool.HoldWorktreeEditor(filepath.Join(y.Path, dir), repo, id)
		return err
	})
	if err != nil {
		return "", err
	}
	a.wmu.Lock()
	defer a.wmu.Unlock()
	if ctx.Err() != nil || a.editorEpoch != epoch || a.win != win {
		release()
		return "", errors.New("the file editor was closed")
	}
	if a.editorHolds == nil {
		a.editorHolds = make(map[string]func())
	}
	token := rand.Text()
	a.editorHolds[token] = release
	return token, nil
}

func (s *service) EndWorktreeEdit(token string) {
	a := s.app
	a.wmu.Lock()
	defer a.wmu.Unlock()
	if release := a.editorHolds[token]; release != nil {
		delete(a.editorHolds, token)
		release()
	}
}

func (a *app) resetWorktreeEditors(win *mygo.Window) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	if a.win == win {
		a.clearWorktreeEditorsLocked()
	}
}

func (a *app) clearWorktreeEditorsLocked() {
	a.editorEpoch++
	for _, release := range a.editorHolds {
		release()
	}
	a.editorHolds = nil
}
