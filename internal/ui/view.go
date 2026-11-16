package ui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// App is the window's state: what it shows and what the user chose.
//
// MyGo builds the view from this on every frame, so everything that must
// outlive a frame lives here and never in the builders.
type App struct {
	// Store holds the traffic, fed from the core's stream.
	Store *Store
	// Filter narrows the list.
	Filter Filter
	// Search is the text in the filter box.
	Search string

	// Selected is the row the user chose, -1 for none.
	Selected int
	// SelectedID is the session that row held when it was chosen. New traffic
	// arrives at the front and pushes every row down, so a row number does not
	// name a session for long: the choice follows the session by ID instead.
	SelectedID uint64
	// Detail is the tab the detail panel shows.
	Detail int
	// SidebarWidth and MainWidth are the two splits' first panes, which the
	// dividers adjust: the sidebar, and the traffic list.
	SidebarWidth, MainWidth float32
	// WindowWidth and WindowHeight are the window's own size, recorded when it
	// closes so the next run opens the same shape.
	WindowWidth, WindowHeight float32

	// Paused is the message waiting at a breakpoint, if one is.
	Paused *Paused

	// ProxyAddr and CAInstalled describe the core, for the toolbar.
	ProxyAddr   string
	CAInstalled bool
	// Recording reports whether the core is capturing.
	Recording bool
	// Version is the core's version, shown in the status bar.
	Version string
	// CoreMemoryMB and CoreThreads describe the core process.
	CoreMemoryMB, CoreThreads int
	// PausedCount is how many messages the core holds at a breakpoint, and
	// RuleCount how many rules are configured.
	PausedCount, RuleCount int
	// down is why the core is unreachable, empty when it is.
	down string

	// OnToggleCapture, OnResume and OnAbort are what the toolbar and the
	// breakpoint banner call. They are set by the command, so the view asks for
	// an action instead of knowing how to reach the core.
	OnToggleCapture func(on bool)
	OnResume        func(pauseID uint64)
	OnAbort         func(pauseID uint64)

	// ProxyRunning says whether the proxy is listening, and PortAddr where.
	// PortText is what the user typed in the port field.
	ProxyRunning bool
	PortAddr     string
	PortText     string
	// PortError is why the last start failed, empty when it did not.
	PortError string
	// OnStartProxy and OnStopProxy act on the proxy's port. The address comes
	// from PortText, so it may be a port, an address, or empty for any free
	// one.
	OnStartProxy func(addr string)
	OnStopProxy  func()

	// backend and control are the core, as the command passed it.
	backend Backend
	control Control
	// OnRowBuilt, when set, is called for every row the table asks the app to
	// build. It exists so a test can check the table asks for the rows that
	// exist and no more.
	OnRowBuilt func(row int)

	// detail and detailID hold the chosen session's bodies, which are fetched
	// when it is chosen and kept until another is: a list carries none, and
	// re-fetching on every frame would be a request per frame.
	bodies   Detail
	detailID uint64
	// detailErr is why the last fetch failed, empty when it did not.
	detailErr string
	// detailKey names the body widgets, so that switching sessions starts a
	// body pane fresh rather than reusing the previous one's scroll.
	detailKey uint64

	// visible caches the indices the filter lets through, so that a frame
	// does not re-filter a list of thousands twice.
	visible      []int
	visibleDirty bool
	hostList     []HostCount
	hostsDirty   bool
	// list is the table's own state, which must outlive a frame.
	list *ui.ListState
}

// Paused is a message held at a breakpoint.
type Paused struct {
	ID        uint64
	SessionID uint64
	Phase     string
	RuleName  string
	Method    string
	URL       string
}

// NewApp returns an app with the window's defaults.
func NewApp(store *Store) *App {
	return &App{
		Store:        store,
		Selected:     -1,
		Detail:       0,
		SidebarWidth: 184,
		MainWidth:    800,
		Recording:    true,
		visibleDirty: true,
		hostsDirty:   true,
	}
}

// SetCoreStatus records what the core says about itself, for the toolbar and
// the status bar.
func (a *App) SetCoreStatus(st Status) {
	a.ProxyAddr = st.ProxyAddr
	a.Recording = st.Capturing
	a.Version = st.Version
	a.PausedCount = st.Paused
	a.RuleCount = st.Rules
	a.CoreMemoryMB = int(st.MemoryMB)
	a.CoreThreads = st.Goroutines
	a.down = ""
}

// SetCoreDown records that the core cannot be reached, which the toolbar shows
// rather than leaving a stale "recording" light on.
func (a *App) SetCoreDown(err error) {
	a.down = err.Error()
	a.Recording = false
}

// SetCapturing records that capture was turned on or off.
func (a *App) SetCapturing(on bool) {
	a.Recording = on
	a.down = ""
}

// SetPause records the message waiting at a breakpoint, which the banner shows.
func (a *App) SetPause(p *Pause) {
	a.Paused = &Paused{
		ID: p.ID, SessionID: p.SessionID, Phase: p.Phase,
		RuleName: p.RuleName, Method: p.Method, URL: p.URL,
	}
}

// ClearPause removes the banner, when nothing is waiting any more.
func (a *App) ClearPause() { a.Paused = nil }

// Down reports why the core is unreachable, empty when it is reachable.
func (a *App) Down() string { return a.down }

// Invalidate tells the app its filter or data changed.
func (a *App) Invalidate() {
	a.visibleDirty = true
	a.hostsDirty = true
}

// InvalidateAppend tells the app one session was added at the newest end,
// which is the shape of an arriving request.
//
// It extends the filtered rows instead of refiltering them: a burst of requests
// is what a proxy produces, and walking thousands of rows per arrival is what
// makes a live list stutter.
//
// It is only valid for an arrival, and only while the store is still growing.
// A full store does not append: it overwrites its oldest row in place, so the
// number of rows does not change and shifting the visible indices by one puts
// the last one past the end — a blank row that still took the pointer. That case
// falls back to a full invalidation, which is what an eviction really is.
func (a *App) InvalidateAppend(id uint64) {
	a.hostsDirty = true
	if a.visibleDirty || a.visible == nil {
		// The rows are stale for another reason; the next Visible sorts it out.
		return
	}
	// The new session is the one at the front now, and the caller passes the id
	// it recorded: an update of an older session leaves the rows alone.
	newest, ok := a.Store.At(0)
	if !ok || newest.ID != id {
		return
	}
	// A row is its distance from the newest session, so a new one at the front
	// pushes every existing row down by one — unless the store was full, in
	// which case a row fell off the other end and nothing moved.
	if a.Store.Len() >= a.Store.Limit() {
		a.Invalidate()
		return
	}
	for i := range a.visible {
		a.visible[i]++
	}
	if a.Filter.Match(&newest) {
		a.visible = append(a.visible, 0)
		copy(a.visible[1:], a.visible[:len(a.visible)-1])
		a.visible[0] = 0
	}
}

// isHostChosen reports whether the sidebar's host is the filter's choice.
func (a *App) isHostChosen(host string) bool {
	for _, h := range a.Filter.Hosts {
		if h == host {
			return true
		}
	}
	return false
}

// hosts returns the sidebar's host list, recomputed only when the traffic or
// the filter changed: counting every session and sorting the result is not
// work a frame should repeat while nothing moved.
func (a *App) hosts() []HostCount {
	if !a.hostsDirty && a.hostList != nil {
		return a.hostList
	}
	a.hostList = a.Store.Hosts()
	a.hostsDirty = false
	return a.hostList
}

// Visible returns the rows the filter lets through, as indices into the store.
func (a *App) Visible() []int {
	if !a.visibleDirty && a.visible != nil {
		return a.visible
	}
	// The result is kept until the data or the filter changes, because a frame
	// asks more than once (the table, and the count in the filter bar) and a
	// store of thousands of sessions must not be walked per ask.
	n := a.Store.Len()
	out := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if sess, ok := a.Store.At(i); ok && a.Filter.Match(&sess) {
			out = append(out, i)
		}
	}
	a.visible = out
	a.visibleDirty = false
	return out
}

// SelectedSession returns the chosen session.
func (a *App) SelectedSession() (Session, bool) {
	rows := a.Visible()
	if a.Selected < 0 || a.Selected >= len(rows) {
		return Session{}, false
	}
	return a.Store.At(rows[a.Selected])
}

// reselectChosen moves the chosen row so that it holds SelectedID again, after
// the list shifted under it. It is a no-op when the row already holds it, which
// is the common case: only an arrival at the front moves the rows.
func (a *App) reselectChosen(rows []int) bool {
	for i, idx := range rows {
		if sess, ok := a.Store.At(idx); ok && sess.ID == a.SelectedID {
			a.Selected = i
			return true
		}
	}
	// The chosen session left the list — it was dropped, or the filter hides it.
	// A row that no longer exists must not stay chosen.
	a.Selected = -1
	a.SelectedID = 0
	return false
}

// syncSelectionID records which session the chosen row currently holds, and
// puts the choice back on the session it held before the rows moved.
//
// The two halves are one call because they are one invariant: Selected is a row,
// SelectedID is the session that row means, and a frame starts by making them
// agree again.
func (a *App) syncSelectionID(rows []int) {
	if a.Selected < 0 || a.Selected >= len(rows) {
		if a.Selected >= 0 {
			// Out of range after a filter narrowed the list: nothing is chosen.
			a.Selected = -1
		}
		a.SelectedID = 0
		return
	}
	if a.SelectedID != 0 {
		if sess, ok := a.Store.At(rows[a.Selected]); !ok || sess.ID != a.SelectedID {
			// The rows moved under the choice; find the session again.
			if !a.reselectChosen(rows) {
				return
			}
		}
	}
	if sess, ok := a.Store.At(rows[a.Selected]); ok {
		a.SelectedID = sess.ID
	}
}

// View builds the window's interface for a frame.
func (a *App) View(c *ui.Context) {
	// The theme is set on the context, not passed to the window: it is what
	// every control in the frame reads its colours and metrics from, and a
	// field left at its default would put a framework grey next to our panels.
	t := Dark.Theme()
	c.SetTheme(&t)

	// The filter box writes into Search when a key is typed, which happens
	// while this frame's input is handled; syncing here means the frame that
	// draws the table already sees the new text, rather than one frame later.
	// The comparison is against the filter the rows were built with, so a frame
	// that changed nothing does not rebuild them and a keystroke does.
	if a.Filter.Text != a.Search {
		a.Filter.Text = a.Search
		a.Invalidate()
	}

	p := &Dark
	ui.Column(c).Fill().Background(p.Bg).Children(func() {
		a.toolbar(c, p)
		if a.Paused != nil {
			a.pauseBanner(c, p)
		}
		a.content(c, p)
		a.statusBar(c, p)
	})
}

// toolbar is the top bar: what the core is doing, and the section tabs.
func (a *App) toolbar(c *ui.Context, p *Palette) {
	ui.Row(c).FillWidth().Height(tokenHeightToolbar).PaddingX(tokenSpace4).Gap(tokenSpace3).
		Background(p.Surface).Children(func() {
		a.recordingDot(c, p)
		a.portControl(c, p)
		ca := "未安装"
		if a.CAInstalled {
			ca = "已安装"
		}
		a.chip(c, p, "CA", ca)
		ui.Text(c, "流量").TextColor(p.Text).FontSize(tokenFontUI)
		ui.Text(c, fmt.Sprintf("规则 %d", a.RuleCount)).TextColor(p.TextMuted).FontSize(tokenFontUI)
		if a.Paused != nil {
			ui.Text(c, "断点 1").TextColor(p.Danger).FontSize(tokenFontUI)
		}
	})
}

// recordingDot says whether the core is capturing, in colour and in words, and
// turns capture on and off when clicked.
// recordingDot says what the core is doing, in colour and in words, and turns
// capture on and off when clicked.
//
// The states are three, not two: a proxy that is not listening cannot be
// recording, so a red light there would say the opposite of what is happening.
// Nothing toggles when the proxy is off: there is no traffic to record.
func (a *App) recordingDot(c *ui.Context, p *Palette) {
	colour := p.Danger
	label := "录制中"
	action := "停止录制"
	reachable := a.down == ""
	switch {
	case !reachable:
		// A core that is not reachable must not keep a red light on. The
		// action names the state rather than a toggle, since there is nothing
		// to toggle.
		colour = p.Dim
		label = "核心未连接"
		action = "核心未连接"
	case !a.ProxyRunning:
		colour = p.Dim
		label = "代理未启动"
		action = "代理未启动"
	case !a.Recording:
		colour = p.Warning
		label = "未录制"
		action = "开始录制"
	}
	btn := ui.ButtonBase(c).Label(action)
	btn.Gap(tokenSpace2).Padding(tokenSpace1, tokenSpace2).Radius(tokenRadiusControl)
	btn.Children(func() {
		ui.Row(c).Width(8).Height(8).Radius(4).Background(colour)
		ui.Text(c, label).Bold().FontSize(tokenFontTitle).TextColor(p.Text)
	})
	if btn.Clicked() && reachable && a.ProxyRunning && a.OnToggleCapture != nil {
		a.OnToggleCapture(!a.Recording)
	}
}

// portControl is the proxy's address and its switch.
//
// The proxy is not necessarily running: a window that is only looking at traffic
// from another source, or one just opened, has nothing listening yet, and the
// user chooses the port rather than being given one that might be taken. The
// field is editable while stopped and shows the address while running.
func (a *App) portControl(c *ui.Context, p *Palette) {
	ui.Row(c).Height(26).PaddingX(tokenSpace3).Gap(tokenSpace2).Radius(tokenRadiusControl).
		Background(p.Raised).Children(func() {
		if a.ProxyRunning {
			ui.Text(c, "代理").FontSize(tokenFontBody).TextColor(p.TextMuted)
			ui.Text(c, a.PortAddr).FontSize(tokenFontBody).Font(monoFamily).
				TextColor(p.Success).SingleLine()
			if ui.Button(c, "停止").Clicked() && a.OnStopProxy != nil {
				a.OnStopProxy()
			}
			return
		}
		ui.Text(c, "代理").FontSize(tokenFontBody).TextColor(p.TextMuted)
		input := ui.TextInput(c, &a.PortText).
			Label("代理端口").Placeholder("端口或地址，留空随机").Width(150)
		if input.Submitted() && a.OnStartProxy != nil {
			a.OnStartProxy(a.PortText)
		}
		if ui.PrimaryButton(c, "启动").Clicked() && a.OnStartProxy != nil {
			a.OnStartProxy(a.PortText)
		}
		if a.PortError != "" {
			ui.Text(c, a.PortError).FontSize(tokenFontSmall).
				TextColor(p.Danger).SingleLine().MaxWidth(220)
		}
	})
}

// chip is a labelled value in the toolbar, as the proxy's address is.
func (a *App) chip(c *ui.Context, p *Palette, label, value string) {
	ui.Row(c).Height(26).PaddingX(tokenSpace3).Gap(tokenSpace2).Radius(tokenRadiusControl).
		Background(p.Raised).Border(1, p.Border).Children(func() {
		ui.Text(c, label).FontSize(tokenFontBody).TextColor(p.TextMuted)
		ui.Text(c, value).FontSize(tokenFontBody).Font(monoFamily).TextColor(p.Text)
	})
}

// pauseBanner appears only while a breakpoint holds a message: a modal would
// hide the list the user needs to judge the decision by.
func (a *App) pauseBanner(c *ui.Context, p *Palette) {
	ui.Row(c).FillWidth().Height(34).PaddingX(tokenSpace4).Gap(tokenSpace3).
		Background(ui.RGBA(0xf8, 0x51, 0x49, 0x1a)).Children(func() {
		ui.Text(c, "已暂停").Bold().FontSize(tokenFontBody).TextColor(p.Danger)
		ui.Text(c, fmt.Sprintf("断点「%s」拦下了 %s %s",
			a.Paused.RuleName, a.Paused.Method, a.Paused.URL)).
			FontSize(12).SingleLine().TextColor(p.Text)
		ui.Row(c).Grow(1)
		pauseID := a.Paused.ID
		if ui.Button(c, "中止").Clicked() && a.OnAbort != nil {
			a.OnAbort(pauseID)
		}
		if ui.PrimaryButton(c, "放行").Clicked() && a.OnResume != nil {
			a.OnResume(pauseID)
		}
	})
}

// mainArea is the sidebar and the traffic list, side by side.
func (a *App) mainArea(c *ui.Context, p *Palette) {
	ui.Split(c, &a.SidebarWidth, func() {
		a.sidebar(c, p)
	}, func() {
		a.trafficList(c, p)
	}).Fill()
}

// sidebar lists the sessions, hosts and devices to narrow the traffic by.
func (a *App) sidebar(c *ui.Context, p *Palette) {
	ui.Column(c).Fill().Background(p.Surface).PaddingY(tokenSpace3).Children(func() {
		a.sidebarSection(c, p, "会话")
		a.sidebarItem(c, p, "全部流量", a.Store.Len(), len(a.Filter.Hosts) == 0, func() {
			a.Filter.Hosts = nil
			a.Invalidate()
		})
		a.sidebarSection(c, p, "域名")
		for _, h := range a.hosts() {
			host := h.Host
			a.sidebarItem(c, p, host, h.Count, a.isHostChosen(host), func() {
				// Choosing a host narrows the list to it; choosing it again
				// clears the choice, so one row both filters and unfilters.
				if a.isHostChosen(host) {
					a.Filter.Hosts = nil
				} else {
					a.Filter.Hosts = []string{host}
				}
				a.Invalidate()
			})
		}
	})
}

// sidebarSection labels a group of sidebar items.
func (a *App) sidebarSection(c *ui.Context, p *Palette, title string) {
	ui.Text(c, title).FontSize(tokenFontLabel).Bold().
		TextColor(p.Dim).Padding(tokenSpace4, 10, tokenSpace1, 0)
}

// sidebarItem is one clickable row in the sidebar. The label names it for a
// screen reader and for a test; onPress runs when the row is chosen.
func (a *App) sidebarItem(c *ui.Context, p *Palette, label string, count int, active bool, onPress func()) {
	btn := ui.ButtonBase(c).Label(label)
	e := btn.FillWidth().Height(tokenHeightRow).PaddingX(tokenSpace4).Gap(tokenSpace3)
	if active {
		e.Background(p.Hover)
	}
	e.Children(func() {
		colour := p.TextMuted
		if active {
			colour = p.Text
		}
		ui.Text(c, label).FontSize(tokenFontUI).SingleLine().TextColor(colour)
		ui.Row(c).Grow(1)
		ui.Text(c, fmt.Sprint(count)).FontSize(tokenFontSmall).Font(monoFamily).TextColor(p.Dim)
	})
	if btn.Clicked() && onPress != nil {
		onPress()
	}
}

// trafficList is the table of sessions, and the window's main subject.
func (a *App) trafficList(c *ui.Context, p *Palette) {
	ui.Column(c).Fill().Background(p.Bg).Children(func() {
		a.filterBar(c, p)
		a.table(c, p)
	})
}

// filterBar is where the user narrows the list.
func (a *App) filterBar(c *ui.Context, p *Palette) {
	ui.Row(c).FillWidth().Height(tokenHeightBar).PaddingX(tokenSpace3).Gap(tokenSpace3).
		Background(p.Surface).Children(func() {
		// The label names the box for a screen reader and for a test that
		// clicks it; the placeholder is only a hint about what it accepts.
		input := ui.TextInput(c, &a.Search).
			Label("过滤").Placeholder("过滤 URL、方法或状态码").Grow(1)
		if input.Changed() {
			// The typed text narrows the list as it is typed; the table below
			// is built after this, so it shows the new filter in this frame.
			a.Filter.Text = a.Search
			a.Invalidate()
		}
		ui.Text(c, fmt.Sprintf("%d 条", len(a.Visible()))).FontSize(tokenFontData).
			Font(monoFamily).TextColor(p.Dim)
	})
}

// columns are the traffic list's columns. Widths are fixed except the path,
// which takes the room the rest leave.
var columns = []ui.TableColumn{
	{Title: "#", ID: "id", Width: 40, Align: ui.End},
	{Title: "方法", ID: "method", Width: 62},
	{Title: "状态", ID: "status", Width: 48},
	{Title: "域名", ID: "host", Width: 128},
	{Title: "路径", ID: "path", MinWidth: 140},
	// A numeric column is end-aligned, and a header in a narrow end-aligned
	// column is clipped at the edge it is aligned to; the titles are short
	// enough to fit, and the columns are wider than the values they hold.
	{Title: "大小", ID: "size", Width: 66, Align: ui.End},
	{Title: "耗时", ID: "time", Width: 68, Align: ui.End},
}

// table draws the sessions, one row per match.
func (a *App) table(c *ui.Context, p *Palette) {
	rows := a.Visible()
	// The framework writes the chosen row number straight into Selected, so the
	// session behind it has to be recorded once per frame: the next frame can
	// then put the choice back on that session after an arrival pushed the rows
	// down. Without this the selection drifted to whatever row the new traffic
	// left at the same number.
	a.syncSelectionID(rows)
	if a.list == nil {
		a.list = &ui.ListState{}
		// A chosen row is filled with a subdued accent rather than the accent
		// itself: a table with one row in full blue is tiring to read, and a
		// log is scanned for hours. The text keeps its own colours, so a status
		// code stays green or red while chosen.
		a.list.CellsStyle = ui.CellsStyle{
			Row:      p.Selection,
			Cell:     p.Selection,
			CellText: p.Text,
		}
	}
	a.list.Selected = &a.Selected
	// A row's identity is its session ID, so that the chosen row stays chosen
	// as new traffic arrives above it.
	a.list.Key = func(row int) any {
		// A row past the filtered set has no session; its key must still be
		// stable and distinct, or the table's row state (the choice, the
		// pointer) lands on the wrong row. Indexing rows[row] unguarded would
		// panic there instead.
		if row < 0 || row >= len(rows) {
			return negativeKey(row)
		}
		if sess, ok := a.Store.At(rows[row]); ok {
			return sess.ID
		}
		return negativeKey(row)
	}
	ui.Table(c, a.list, columns, len(rows), func(row, col int) {
		if row < 0 || row >= len(rows) {
			return
		}
		sess, ok := a.Store.At(rows[row])
		if !ok {
			return
		}
		// A chosen row keeps its colours: the selection is a subdued fill
		// behind the text, not a different palette. A status code stays red
		// when chosen, which is the whole point of colouring it.
		if a.OnRowBuilt != nil {
			a.OnRowBuilt(row)
		}
		chosen := row == a.Selected
		fg := func(normal ui.Color) ui.Color { return normal }
		switch col {
		case 0:
			ui.Text(c, fmt.Sprint(sess.ID)).FontSize(tokenFontData).Font(monoFamily).
				TextColor(fg(p.Dim)).SingleLine()
		case 1:
			ui.Text(c, sess.Method).FontSize(tokenFontData).Font(monoFamily).
				TextColor(fg(p.MethodColor(sess.Method))).SingleLine()
		case 2:
			a.statusCell(c, p, &sess, chosen)
		case 3:
			ui.Text(c, sess.Host).FontSize(tokenFontData).Font(monoFamily).
				TextColor(fg(p.TextMuted)).SingleLine()
		case 4:
			a.pathCell(c, p, &sess, chosen)
		case 5:
			ui.Text(c, sess.Size()).FontSize(tokenFontData).Font(monoFamily).
				TextColor(fg(p.TextMuted)).SingleLine()
		case 6:
			ui.Text(c, sess.Duration()).FontSize(tokenFontData).Font(monoFamily).
				TextColor(fg(p.TextMuted)).SingleLine()
		}
	}).Grow(1)
}

// statusCell writes the status code in its family's colour, or the phase while
// the exchange is still in flight.
func (a *App) statusCell(c *ui.Context, p *Palette, sess *Session, chosen bool) {
	text := fmt.Sprint(sess.Status)
	if !sess.Done() {
		text = "…"
	} else if sess.Status == 0 {
		text = "ERR"
	}
	colour := p.StatusColor(sess.Status)
	ui.Text(c, text).FontSize(tokenFontData).Font(monoFamily).
		TextColor(colour).SingleLine()
}

// pathCell writes the path, with a badge for a session a rule touched: the one
// thing worth knowing before clicking a row.
func (a *App) pathCell(c *ui.Context, p *Palette, sess *Session, chosen bool) {
	pathColour := p.Text
	ui.Row(c).Gap(6).Children(func() {
		ui.Text(c, sess.Path()).FontSize(tokenFontData).Font(monoFamily).
			TextColor(pathColour).SingleLine().Grow(1)
		switch sess.Source {
		case "mocked":
			a.badge(c, p, "Mock", p.Accent)
		case "aborted":
			a.badge(c, p, "中止", p.Danger)
		}
	})
}

// badge is a small label beside a row's path.
func (a *App) badge(c *ui.Context, p *Palette, text string, colour ui.Color) {
	ui.Text(c, text).FontSize(tokenFontMicro).PaddingX(5).Radius(tokenRadiusBadge).
		TextColor(colour).SingleLine()
}

// negativeKey is a row key for a row that has no session, kept out of the range
// sessions use so it cannot collide with one.
func negativeKey(row int) int { return -1 - row }
