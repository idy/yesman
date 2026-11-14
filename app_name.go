package main

import (
	"log"

	"github.com/egoist/mygo"
)

const appDisplayName = "Yes Man"

// setAppDisplayName runs after taking the legacy single-instance lock.
// Resolve MyGo's name-derived paths while the stable identity is still in
// effect. SetPath then preserves locale, window state, cache, and logs.
// The application's own data/storage/vault paths already use stable IDs.
func setAppDisplayName() error {
	if appName != "yesman" {
		return nil // explicitly isolated e2e identity
	}
	for _, name := range []mygo.PathName{mygo.PathUserData, mygo.PathCache, mygo.PathLogs} {
		path, err := mygo.App.Path(name)
		if err != nil {
			return err
		}
		mygo.App.SetPath(name, path)
	}
	mygo.App.SetName(appDisplayName)
	if err := refreshInstalledAppName(); err != nil {
		log.Print("installed app name: ", err)
	}
	return nil
}
