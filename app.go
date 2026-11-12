package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
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
)

type app struct {
	home   home.Home
	pool   *pool.Pool
	yesmen *yesmen.Store
	agents *agent.Manager
	pi     string
	piErr  error

	wmu  sync.Mutex
	win  *mygo.Window
	tray *mygo.Tray

	smu     sync.Mutex
	last    map[string]agent.Status
	release func() // ends KeepRunning while no agent works

	fmu          sync.Mutex
	pendingFocus string // the agent a notification click asked to show
}

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
		StateKey:      "main",
	})
	a.win = w
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
	t, err := mygo.NewTray(mygo.TrayOptions{Icon: trayIcon(), IconIsTemplate: true, ToolTip: "yesman"})
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

// trayIcon draws a "Y", black on transparent for macOS to tint, and
// slate blue elsewhere so it shows on light and dark taskbars.
func trayIcon() []byte {
	const size = 36
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	ink := color.NRGBA{R: 74, G: 99, B: 160, A: 255}
	if runtime.GOOS == "darwin" {
		ink = color.NRGBA{A: 255}
	}
	type seg struct{ x1, y1, x2, y2 float64 }
	segs := []seg{{9, 7, 18, 19}, {27, 7, 18, 19}, {18, 19, 18, 30}}
	const half = 1.9
	for y := range size {
		for x := range size {
			px, py := float64(x)+0.5, float64(y)+0.5
			d := math.Inf(1)
			for _, s := range segs {
				d = math.Min(d, distance(px, py, s.x1, s.y1, s.x2, s.y2))
			}
			cover := math.Max(0, math.Min(1, half+0.5-d))
			if cover > 0 {
				c := ink
				c.A = uint8(float64(ink.A) * cover)
				img.SetNRGBA(x, y, c)
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// distance from a point to a segment.
func distance(px, py, x1, y1, x2, y2 float64) float64 {
	dx, dy := x2-x1, y2-y1
	t := ((px-x1)*dx + (py-y1)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(x1+t*dx), py-(y1+t*dy))
}
