// yesman is a tray app that hires yesmen: pi agents that work in a
// workspace of their own, check repositories out of a shared repo pool,
// share their knowledge, and start workers.
//
//	go run .                      # the app, with its window
//	YESMAN_HOME=/tmp/y go run .   # with its data somewhere else
package main

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

//go:embed all:web
var webFS embed.FS

//go:embed pi/yesman.ts
var extensionSource []byte

func main() {
	mygo.App.SetName("yesman")
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance shows its window
	}
	importLoginPath()

	h, storage, err := openStorage()
	if err != nil {
		log.Fatal(err)
	}
	if err := h.Ensure(); err != nil {
		log.Fatal(err)
	}
	extension := filepath.Join(h.Runtime(), "pi", "yesman.ts")
	if err := os.MkdirAll(filepath.Dir(extension), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(extension, extensionSource, 0o644); err != nil {
		log.Fatal(err)
	}

	a := &app{home: h, storage: storage, pool: pool.New(h.Repos()), yesmen: yesmen.New(h.Yesmen(), h.Archive())}
	a.pi, a.piErr = findPi()
	a.settings = loadSettings(h.Settings())
	a.updates = newUpdates()
	if a.settings.Appearance != "" {
		mygo.Theme.SetSource(a.settings.Appearance) // before Run, so the window opens in it
	}

	a.clones = newClones(addRepo(a.pool), func(list []Clone, arrived bool) {
		_ = clonesEvent.Broadcast(list)
		if arrived {
			a.notifyChanged()
		}
	})

	a.fetcher = newFetcher(a.pool, func(s FetchState) { _ = fetchesEvent.Broadcast(s) })
	a.fetcher.SetInterval(time.Duration(a.settings.AutoFetch) * time.Minute)
	go a.fetcher.Schedule(context.Background())

	srv := &server.Server{Pool: a.pool, Yesmen: a.yesmen, Changed: a.notifyChanged, AddRepo: a.clones.Add,
		BoardChanged: func(yesman string) { _ = boardEvent.Broadcast(yesman) }}
	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	a.agents = agent.New(agent.Config{
		Pi:        a.pi,
		Extension: extension,
		Env:       []string{"YESMAN_API=" + srv.URL(), "YESMAN_HOME=" + h.Root},
		Yesmen:    a.yesmen,
		OnEvent:   func(e agent.Event) { _ = agentEvent.Broadcast(e) },
		OnChange:  a.agentsChanged,
	})
	srv.Agents = a.agents

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	if dir := os.Getenv("YESMAN_WEB_DIR"); dir != "" {
		web = os.DirFS(dir) // edit the frontend without rebuilding
	}
	mygo.SetFrontend(web)
	mygo.BindAs("Yesman", &service{app: a})

	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	mygo.App.OnWindowAllClosed(func() {}) // keep running in the tray
	mygo.App.OnSecondInstance(func([]string, string) { a.showWindow() })
	mygo.App.OnActivate(func(bool) { a.showWindow() })
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { a.agents.StopAll() })
	mygo.App.WhenReady(func() {
		if err := mygo.App.Dock.SetIcon(appIcon); err != nil {
			log.Print("dock icon: ", err)
		}
		a.setupTray()
		a.updates.start()
		if os.Getenv("YESMAN_HIDDEN") == "" {
			a.showWindow()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// findPi locates the pi executable: $YESMAN_PI, else pi on the PATH.
func findPi() (string, error) {
	if p := os.Getenv("YESMAN_PI"); p != "" {
		return p, nil
	}
	p, err := exec.LookPath("pi")
	if err != nil {
		return "pi", fmt.Errorf("pi is not installed or not on the PATH: install it with npm install -g @earendil-works/pi-coding-agent")
	}
	return p, nil
}
