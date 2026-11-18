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
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/githubapp"
	"github.com/idy/yesman/internal/piruntime"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/schedule"
	"github.com/idy/yesman/internal/secrets"
	"github.com/idy/yesman/internal/server"
	"github.com/idy/yesman/internal/yesmen"
)

//go:embed all:web
var webFS embed.FS

//go:embed pi/yesman.ts
var extensionSource []byte

// appName is the stable single-instance and profile identity. Its display
// name is set separately, after taking the lock that old releases use.
var appName = "yesman"

func main() {
	// MyGo's binding generator only needs method types. Do not initialize
	// a user's storage, vault, agents, or profile while inspecting that API.
	if os.Getenv("MYGO_GENERATE") != "" {
		mygo.BindAs("Yesman", &service{})
		if err := mygo.App.Run(); err != nil {
			log.Fatal(err)
		}
		return
	}
	if githubapp.IsShim(os.Args) {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "--yesman-github-app-gh" {
			args = args[1:]
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := githubapp.RunShim(ctx, args, os.Stdin, os.Stdout, os.Stderr)
		cancel()
		os.Exit(code)
	}
	mygo.App.SetName(appName)
	mygo.App.SetLanguages(languages...)
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance shows its window
	}
	if err := setAppDisplayName(); err != nil {
		log.Fatal(err)
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
	if err := a.yesmen.EnsureTaskLogs(); err != nil {
		log.Print("workspace working logs: ", err)
	}
	a.pi, a.piErr = findPi()
	var piCommand func(context.Context) (string, []string, error)
	var managedRuntime *piruntime.Runtime
	if os.Getenv("YESMAN_PI") == "" && (a.piErr != nil || piruntime.OfficialNPM(a.pi)) {
		a.managedPi = true
		managed, err := piruntime.New(h.Runtime())
		a.piErr = err
		if err == nil {
			a.pi = managed.Description()
			piCommand = managed.Command
			managedRuntime = managed
			a.piRuntime = managed
		}
	}
	a.runPi = runPi(a.pi, h.Runtime())
	if piCommand != nil {
		a.runPi = runPiCommand(piCommand, h.Runtime())
	}
	a.piJobs = newPiJobs(a.runPi, func(j PiJobs) { _ = piJobsEvent.Broadcast(j) })
	a.settings = loadSettings(h.Settings())
	a.setTheme = mygo.Theme.SetSource
	a.chosenLocale, a.setLocale = mygo.App.ChosenLocale, mygo.App.SetLocale
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

	a.vault = secrets.NewVault(h.Secrets())
	a.variables = secrets.NewVariables(h.Variables())
	a.keychain = appKeychain()
	go a.unlockFromKeychain() // the system may ask the user first
	gh, ghErr := exec.LookPath("gh")
	if ghErr != nil {
		gh = ""
	} else if absolute, err := filepath.Abs(gh); err == nil {
		gh = absolute
	} else {
		gh = ""
	}
	executable, _ := os.Executable()
	shim, routingErr := a.prepareGitHubAppRouting(gh, executable)
	if routingErr != nil {
		log.Print("GitHub App command routing is unavailable: ", routingErr)
	}
	routingRequired := ""
	if shim == "" && a.githubApp.RequiresRouting() {
		routingRequired = "1"
	}
	if a.githubApp.Configured() {
		go func() { _ = a.githubApp.Prepare("") }()
	}
	srv := &server.Server{Pool: a.pool, Yesmen: a.yesmen, Changed: a.notifyChanged, AddRepo: a.clones.Add,
		BoardChanged: func(yesman string) {
			_ = boardEvent.Broadcast(yesman)
			if a.agents != nil {
				a.agents.TasksChanged(yesman) // a task done may let another go on
			}
		},
		NeedsYouChanged: func(yesman string, added *yesmen.Handoff) { a.needsYouChanged(yesman, added) },
		TopicsChanged:   a.topicsChanged,
		Secrets:         a.vault, Variables: a.variables, Crew: a.leadCrew, GitHubApp: a.githubApp}
	if err := srv.Start(); err != nil {
		log.Fatal(err)
	}
	var schedules atomic.Pointer[schedule.Service]
	a.agents = agent.New(agent.Config{
		Pi:        a.pi,
		Command:   piCommand,
		Extension: extension,
		Env:       []string{"YESMAN_API=" + srv.URL(), "YESMAN_HOME=" + h.Root, "YESMAN_GH_SHIM=" + shim, "YESMAN_GH_REAL=" + gh, "YESMAN_GH_REQUIRED=" + routingRequired},
		Yesmen:    a.yesmen,
		OnEvent: func(e agent.Event) {
			_ = agentEvent.Broadcast(e)
			if s := schedules.Load(); s != nil {
				s.OnAgentEvent(e) // a scheduled run's receipt
			}
		},
		OnChange: a.agentsChanged,
	})
	srv.Agents = a.agents
	// Scheduled tasks run while the app runs, in the tray too; one app per
	// data directory runs them.
	a.schedules = schedule.New(schedule.Config{
		Yesmen:       a.yesmen,
		Agents:       a.agents,
		LockPath:     filepath.Join(h.Runtime(), "schedules.lock"),
		OnChange:     func(yesman string) { _ = schedulesEvent.Broadcast(yesman) },
		BoardChanged: func(yesman string) { _ = boardEvent.Broadcast(yesman) },
	})
	schedules.Store(a.schedules)
	srv.Schedules = a.schedules
	a.schedules.Start()
	a.countHandoffs() // for the tray, before the first change of the agents
	a.catalog = newCatalog(func(ctx context.Context) ([]agent.Model, error) {
		if a.piErr != nil {
			return nil, a.piErr
		}
		return a.agents.Catalog(ctx, h.Runtime())
	})
	go a.catalog.get(context.Background(), false) // for the first lead's crew

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
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		a.schedules.Close() // no new run starts while the agents stop
		a.stopGitHubAppSetup()
		a.githubApp.Close()
		if managedRuntime != nil {
			managedRuntime.Close()
		}
		a.agents.StopAll()
	})
	mygo.App.WhenReady(func() {
		if err := mygo.App.Dock.SetIcon(appIcon); err != nil {
			log.Print("dock icon: ", err)
		}
		a.setupTray()
		a.updates.start()
		// Tell each lead of work nobody carries since the app last ran: a
		// trigger lost when it quit is not lost. Leads the user stopped,
		// and tasks told about already, stay as they are.
		a.agents.Resume()
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
