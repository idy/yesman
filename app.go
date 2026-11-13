package main

import (
	"log"
	"runtime"
	"strconv"
	"sync"

	"github.com/egoist/mygo"

	"github.com/idy/yesman/internal/agent"
	"github.com/idy/yesman/internal/home"
	"github.com/idy/yesman/internal/pool"
	"github.com/idy/yesman/internal/yesmen"
)

// Events the page listens to with mygo.on(name, fn).
var (
	// agentEvent carries every record of every agent's pi process.
	agentEvent = mygo.NewEvent[agent.Event]("agent-event")
	// agentsEvent carries all agents whenever one's status changes.
	agentsEvent = mygo.NewEvent[[]agent.Info]("agents")
	// changedEvent says the pool, the yesmen or their worktrees changed.
	changedEvent = mygo.NewEvent[bool]("changed")
	// focusEvent asks the page to show an agent, after a notification click.
	focusEvent = mygo.NewEvent[string]("focus-agent")
	// clonesEvent carries the repositories on their way into the pool
	// whenever one starts, finishes or fails.
	clonesEvent = mygo.NewEvent[[]Clone]("clones")
	// fetchesEvent carries where fetching the pool stands whenever a fetch
	// starts or ends.
	fetchesEvent = mygo.NewEvent[FetchState]("fetches")
)

type app struct {
	home    home.Home
	pool    *pool.Pool
	clones  *clones
	fetcher *fetcher
	yesmen  *yesmen.Store
	agents  *agent.Manager
	pi      string
	piErr   error

	wmu  sync.Mutex
	win  *mygo.Window
	tray *mygo.Tray

	smu     sync.Mutex
	last    map[string]agent.Status
	release func() // ends KeepRunning while no agent works

	fmu          sync.Mutex
	pendingFocus string // the agent a notification click asked to show

	setmu    sync.Mutex
	settings Settings

	updates *updates

	files fileWatch // the workspace whose files the page shows
}

// titlebarHeight is the height of the titlebar the page draws.
const titlebarHeight = 40

// showWindow opens the main window, or brings it to the front.
func (a *app) showWindow() {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	if a.win != nil {
		if a.win.IsMinimized() {
			a.win.Restore()
		}
		a.win.Show()
		a.win.Focus()
		mygo.App.Focus()
		return
	}
	// The app lives in the tray; it shows in the Dock and app switcher only
	// while its window is open.
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyRegular)
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:         "yesman",
		URL:           "/",
		Width:         1180,
		Height:        780,
		TitleBarStyle: mygo.TitleBarHidden,
		// The page's titlebar is 40px tall: Windows' caption buttons fill
		// it, and the traffic lights sit centered in it.
		TitleBarHeight:       titlebarHeight,
		TrafficLightPosition: &mygo.Point{X: 16, Y: (titlebarHeight - 14) / 2},
		StateKey:             "main",
	})
	if runtime.GOOS != "darwin" {
		if err := w.SetIcon(appIcon); err != nil {
			log.Print("window icon: ", err)
		}
	}
	a.win = w
	w.Page().OnContextMenu(func(e *mygo.ContextMenuEvent) {
		// App rows provide their own menus. Elsewhere retain native text
		// editing actions without exposing browser navigation/debug items.
		e.Menu.Remove(mygo.ContextMenuReload, mygo.ContextMenuInspect,
			mygo.ContextMenuBack, mygo.ContextMenuForward, mygo.ContextMenuStop)
	})
	w.OnClosed(func() {
		a.wmu.Lock()
		if a.win == w {
			a.win = nil
		}
		a.wmu.Unlock()
		mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
	})
	mygo.App.Focus()
}

func (a *app) windowFocused() bool {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return a.win != nil && a.win.IsVisible() && a.win.IsFocused()
}

func (a *app) setupTray() {
	t, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon, IconIsTemplate: true, ToolTip: "yesman"})
	if err != nil {
		log.Print("tray: ", err)
		return
	}
	a.tray = t
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "Open yesman", Click: func(*mygo.MenuItem, *mygo.Window) { a.showWindow() }},
		mygo.Separator(),
		{Label: "Quit yesman", Click: func(*mygo.MenuItem, *mygo.Window) { mygo.App.Quit() }},
	})
	if runtime.GOOS == "linux" {
		t.SetMenu(menu) // AppIndicator reports no clicks, only menus
	} else {
		t.OnClick(a.showWindow)
		t.OnRightClick(func() {
			t.SetMenu(menu)
			defer t.SetMenu(nil)
			t.PopUpMenu()
		})
	}
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { t.Destroy() })
}

func (a *app) notifyChanged() { _ = changedEvent.Broadcast(true) }

// agentsChanged tells the page, updates the tray, keeps the app awake while
// agents work, and notifies the user when an agent needs them.
func (a *app) agentsChanged(list []agent.Info) {
	_ = agentsEvent.Broadcast(list)

	working, waiting := 0, 0
	type note struct{ id, title, body string }
	var notes []note
	a.smu.Lock()
	if a.last == nil {
		a.last = map[string]agent.Status{}
	}
	for _, i := range list {
		prev := a.last[i.ID]
		a.last[i.ID] = i.Status
		switch i.Status {
		case agent.Working:
			working++
		case agent.Waiting:
			waiting++
		}
		if prev == i.Status || prev == "" && i.Status != agent.Waiting {
			continue
		}
		switch {
		case i.Status == agent.Waiting:
			notes = append(notes, note{i.ID, i.ID + " needs you", "It asked a question and is waiting for your answer."})
		case i.Status == agent.Failed:
			notes = append(notes, note{i.ID, i.ID + " stopped", i.Detail})
		case prev == agent.Working && i.Status == agent.Idle && i.Role == agent.RoleLead:
			notes = append(notes, note{i.ID, i.Yesman, "Finished and waiting for you."})
		}
	}
	if working > 0 && a.release == nil {
		a.release = mygo.Power.KeepRunning("yesman agents are working")
	} else if working == 0 && a.release != nil {
		a.release()
		a.release = nil
	}
	a.smu.Unlock()

	if a.tray != nil {
		title := ""
		if waiting > 0 {
			title = strconv.Itoa(waiting)
		}
		a.tray.SetTitle(title)
		a.tray.SetToolTip("yesman: " + strconv.Itoa(working) + " working, " + strconv.Itoa(waiting) + " waiting for you")
	}
	if a.windowFocused() {
		return
	}
	for _, n := range notes {
		id := n.id
		notification := mygo.NewNotification(mygo.NotificationOptions{ID: id, Title: n.title, Body: n.body, Group: id})
		notification.OnClick(func() {
			a.fmu.Lock()
			a.pendingFocus = id
			a.fmu.Unlock()
			a.showWindow()
			_ = focusEvent.Broadcast(id) // an open page shows it now; a new one calls TakeFocus
		})
		_ = notification.Show()
	}
}
