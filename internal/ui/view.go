package ui

import (
	"fmt"
	"strings"

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

	// Section is the sidebar's or the segmented control's choice: which pane the
	// window shows. SectionTraffic is the traffic list; the others are the rule
	// list, the breakpoints and the certificate.
	Section Section
	// SidebarSelection is the sidebar's own id for what is chosen, which the
	// framework's Sidebar writes as a string.
	SidebarSelection string
	// HostsOpen is whether the sidebar's host section is expanded.
	HostsOpen bool
	// ProxyOpen is whether the proxy's popover is showing.
	ProxyOpen bool

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
	// CoreMemoryMB and CoreGoroutines describe the core process. The second is
	// its goroutine count, not its thread count: the API reports goroutines, and
	// calling that "线程" in the status bar was a label that named a different
	// number from the one it showed.
	CoreMemoryMB, CoreGoroutines int
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

// Section is one of the window's panes, chosen in the toolbar.
type Section int

const (
	// SectionTraffic is the captured traffic, which is what the window is for.
	SectionTraffic Section = iota
	// SectionRules is the rules that rewrite traffic.
	SectionRules
	// SectionBreakpoints is the breakpoint rules that pause it.
	SectionBreakpoints
	// SectionCertificate is the authority certificate to install.
	SectionCertificate
)

// String names a section for the segmented control.
func (s Section) String() string {
	switch s {
	case SectionRules:
		return "规则"
	case SectionBreakpoints:
		return "断点"
	case SectionCertificate:
		return "证书"
	}
	return "流量"
}

// sections is the toolbar's segmented control, in order.
var sections = []string{"流量", "规则", "断点", "证书"}

// NewApp returns an app with the window's defaults.
func NewApp(store *Store) *App {
	return &App{
		Store:            store,
		Selected:         -1,
		Detail:           0,
		SidebarWidth:     200,
		MainWidth:        760,
		Recording:        true,
		HostsOpen:        true,
		SidebarSelection: "all",
		visibleDirty:     true,
		hostsDirty:       true,
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
	a.CoreGoroutines = st.Goroutines
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

// toolbar is the window's top bar.
//
// It follows the shape a macOS tool window has: the window's own controls on the
// left, the one thing the pane is about in the middle, and the pane's switch on
// the right. The traffic list's filter lives here rather than in the list,
// because a toolbar is where a Mac user looks for a search field; the proxy's
// port, which changes rarely and takes two controls, is behind a popover so that
// it does not sit in the toolbar of a window whose proxy is running.
func (a *App) toolbar(c *ui.Context, p *Palette) {
	ui.Row(c).FillWidth().Height(tokenHeightToolbar).
		Padding(tokenSpace2, tokenSpace4, tokenSpace2, tokenContentInset).
		Background(p.Surface).AlignItems(ui.Center).Children(func() {
		// Three groups, in the order a tool window puts them: the window's own
		// state on the left, what narrows the pane beside it, and the switch
		// between panes on the right. One growing spacer between the filter and
		// the switch is what pushes the last group to the edge; two spacers put
		// the filter in the middle, which is not where a search field belongs in
		// a window whose left side is a list.
		ui.Row(c).Gap(tokenSpace3).AlignItems(ui.Center).Shrink(0).Children(func() {
			a.recordingSwitch(c, p)
			a.proxyButton(c, p)
		})
		if a.Section == SectionTraffic {
			ui.Row(c).Width(tokenSpace5).Height(1)
			a.filterField(c, p)
		}
		ui.Row(c).Grow(1).Height(1)
		ui.Row(c).Shrink(0).Children(func() {
			a.sectionControl(c, p)
		})
	})
}

// recordingSwitch says whether the core is capturing and turns it on or off.
//
// It is a switch rather than a light with words beside it: whether the proxy is
// recording is a state that is either on or off, and macOS draws that as a
// switch. A core that is not reachable, or a proxy that is not listening, cannot
// record, so the switch is disabled and the words say why.
func (a *App) recordingSwitch(c *ui.Context, p *Palette) {
	// The switch shows the core's own answer, not a value recomputed from the
	// proxy's state: whether the core is capturing and whether anything is
	// listening are two facts, and folding them together made a stopped proxy
	// look like a core that had been switched off. The switch is disabled when
	// there is nothing to record from, which is what "cannot" means.
	live := a.down == "" && a.ProxyRunning
	hint := ""
	switch {
	case a.down != "":
		hint = "核心未连接"
	case !a.ProxyRunning:
		hint = "代理未启动"
	}

	ui.Row(c).Gap(tokenSpace3).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "录制").FontSize(tokenFontUI).TextColor(p.TextMuted)
		on := a.Recording
		wasOn := on
		sw := ui.Switch(c, &on)
		sw.Disabled(!live)
		sw.Label("录制流量")
		// The callback is what changes capture; the value comes back from the
		// core on the next tick, so a toggle the core refused does not leave a
		// switch that lies about it.
		if on != wasOn && a.OnToggleCapture != nil {
			a.OnToggleCapture(on)
		}
		if hint != "" {
			// A note, not a label: it explains the disabled switch, so it sits a
			// gap away in the dimmest level rather than touching the control.
			ui.Text(c, hint).FontSize(tokenFontMicro).TextColor(p.Dim).SingleLine()
		}
	})
}

// proxyButton shows where the proxy listens, and opens its popover.
//
// The address is the one thing a user copies out of this window — into a browser,
// a terminal, an app's proxy settings — so it stays in the toolbar in a
// monospaced face, and only the controls for changing it hide behind the
// popover.
func (a *App) proxyButton(c *ui.Context, p *Palette) {
	open := a.ProxyOpen
	value := "未启动"
	colour := p.TextMuted
	if a.ProxyRunning {
		value = a.PortAddr
		colour = p.Success
	}
	btn := ui.ButtonBase(c).Label("代理设置")
	btn.Height(28).PaddingX(tokenSpace3).Gap(tokenSpace2).Radius(tokenRadiusControl).
		Background(p.Raised).Children(func() {
		ui.Icon(c, iconProxy).Width(14).Height(14).TextColor(p.TextMuted)
		ui.Text(c, value).FontSize(tokenFontBody).Font(monoFamily).
			TextColor(colour).SingleLine()
	})
	// A popover does not open on its own: the button toggles the flag, and the
	// popover draws while it is set. Clicking it again closes it, which is what a
	// toolbar button with a popover does.
	if btn.Clicked() {
		open = !open
	}
	ui.Popover(c, btn, &open, func() {
		a.proxyPopover(c, p)
	})
	a.ProxyOpen = open
}

// proxyPopover is what the proxy button opens: the port, the switch, and the
// state of the certificate.
func (a *App) proxyPopover(c *ui.Context, p *Palette) {
	ui.Column(c).Width(300).Padding(tokenSpace4).Gap(tokenSpace3).Children(func() {
		ui.Text(c, "被调试的客户端把代理指向这里").FontSize(tokenFontSmall).TextColor(p.TextMuted)

		if a.ProxyRunning {
			ui.Row(c).Gap(tokenSpace2).AlignItems(ui.Center).Children(func() {
				ui.Text(c, a.PortAddr).FontSize(tokenFontData).Font(monoFamily).
					TextColor(p.Success).Grow(1).SingleLine()
				if ui.Button(c, "停止").Clicked() && a.OnStopProxy != nil {
					a.OnStopProxy()
					a.ProxyOpen = false
				}
			})
		} else {
			input := ui.TextInput(c, &a.PortText).
				Label("代理端口").Placeholder("端口或地址，留空则随机")
			// The popover stays open after a click: a port that is taken is
			// reported right here, and closing on the click would take the reason
			// away before it was drawn. It closes once the proxy reports that it
			// is listening, which is the answer the user was waiting for.
			if input.Submitted() && a.OnStartProxy != nil {
				a.OnStartProxy(a.PortText)
			}
			if ui.PrimaryButton(c, "启动").Clicked() && a.OnStartProxy != nil {
				a.OnStartProxy(a.PortText)
			}
		}
		if a.PortError != "" {
			ui.Text(c, a.PortError).FontSize(tokenFontSmall).TextColor(p.Danger)
		}

		ui.Divider(c)
		ui.Row(c).Gap(tokenSpace2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconCert).Width(14).Height(14).TextColor(p.TextMuted)
			ui.Text(c, "根证书").FontSize(tokenFontBody).TextColor(p.TextMuted)
			ca, colour := "未安装", p.Warning
			if a.CAInstalled {
				ca, colour = "已安装", p.Success
			}
			ui.Text(c, ca).FontSize(tokenFontBody).TextColor(colour).Grow(1)
		})
	})
}

// sectionControl switches the window between its panes.
func (a *App) sectionControl(c *ui.Context, p *Palette) {
	sel := int(a.Section)
	ui.Segmented(c, &sel, sections...).Height(28)
	if sel != int(a.Section) && sel >= 0 && sel < len(sections) {
		a.Section = Section(sel)
	}
}

// pauseBanner appears only while a breakpoint holds a message: a modal would
// hide the list the user needs to judge the decision by.
func (a *App) pauseBanner(c *ui.Context, p *Palette) {
	// The banner spans the window, above both panes: it is about one exchange,
	// but the decision it asks for is the window's, and a banner inside the list
	// would be pushed around by the list's own scrolling. Its right padding is
	// the toolbar's, so the two buttons line up with the segmented control above
	// them rather than sitting against the window edge.
	ui.Row(c).FillWidth().Height(38).Padding(tokenSpace2, tokenSpace4).
		Gap(tokenSpace3).AlignItems(ui.Center).
		// The fill is a tint of the danger colour, not a wash: at 10% it read as
		// empty space with two buttons in it, and a pause the user cannot see is
		// a pause they miss. 22% is dark enough to stay behind the text and warm
		// enough to register at a glance.
		Background(ui.RGBA(0xf8, 0x51, 0x49, 0.16)).Children(func() {
		ui.Text(c, "已暂停").Bold().FontSize(tokenFontBody).TextColor(p.Danger)
		ui.Text(c, fmt.Sprintf("断点「%s」拦下了 %s %s",
			a.Paused.RuleName, a.Paused.Method, a.Paused.URL)).
			FontSize(tokenFontBody).SingleLine().TextColor(p.Text).Grow(1)
		pauseID := a.Paused.ID
		if ui.Button(c, "中止").Clicked() && a.OnAbort != nil {
			a.OnAbort(pauseID)
		}
		if ui.PrimaryButton(c, "放行").Clicked() && a.OnResume != nil {
			a.OnResume(pauseID)
		}
	})
}

// sidebar is the window's source list.
//
// It is the framework's Sidebar rather than a column of buttons: a source list is
// a thing macOS defines — collapsible sections, a chosen item that is filled
// rather than outlined, arrow keys, type-to-select, a tree role for assistive
// technology — and drawing one by hand gets the look and misses everything else.
func (a *App) sidebar(c *ui.Context, p *Palette) {
	sel := a.SidebarSelection
	if sel == "" {
		sel = "all"
	}
	// The traffic is the only thing a source list can filter; the other panes
	// are lists of their own and bring their sections with them.
	showTraffic := a.Section == SectionTraffic

	// The source list paints the window's material on macOS, so its fill is
	// translucent rather than opaque: the sidebar material is behind it, and an
	// opaque column would hide the one thing that makes a Mac window read as a
	// Mac window. Elsewhere it composites onto the window's own background.
	sb := ui.Sidebar(c, &sel, func() {
		if showTraffic {
			a.sidebarItem(c, "all", iconAll, "全部流量", a.Store.Len())
			ui.SidebarSection(c, "域名", &a.HostsOpen, func() {
				for _, h := range a.hosts() {
					host := h.Host
					a.sidebarItem(c, "host:"+host, iconHost, host, h.Count)
				}
			})
			return
		}
		// The other panes name what they hold, so the list says what is there
		// instead of showing an empty column beside it.
		ui.SidebarSection(c, "本页", nil, func() {
			a.sidebarItem(c, "section", sectionIcon(a.Section), a.Section.String(), a.sectionCount())
		})
	})
	sb.Background(sidebarFill())

	a.applySidebarChoice(sel, showTraffic)
}

// sidebarItem adds one row with its count as a badge.
//
// The count is a Badge, which is what a source list puts beside an item: the
// framework closes the row's children with its own label, and that label already
// takes the remaining width, so anything added after it has to be a thing that
// sizes itself — a spacer would take the room the label needed, and the label
// would be clipped to "api.exa…".
func (a *App) sidebarItem(c *ui.Context, id string, icon *ui.SVG, label string, count int) {
	item := ui.SidebarItem(c, id, icon, label)
	if count > 0 {
		item.Children(func() {
			ui.Badge(c, fmt.Sprint(count))
		})
	}
}

// applySidebarChoice turns the source list's choice into a filter.
func (a *App) applySidebarChoice(sel string, showTraffic bool) {
	if !showTraffic || sel == a.SidebarSelection {
		a.SidebarSelection = sel
		return
	}
	previous := a.SidebarSelection
	a.SidebarSelection = sel

	switch {
	case sel == "all" || sel == "":
		a.Filter.Hosts = nil
	case strings.HasPrefix(sel, "host:"):
		host := strings.TrimPrefix(sel, "host:")
		// Choosing the chosen host again clears the filter, so that one row both
		// narrows the list and widens it.
		if previous == sel {
			a.Filter.Hosts = nil
			return
		}
		a.Filter.Hosts = []string{host}
	default:
		return
	}
	a.Invalidate()
}

// sectionIcon is the icon a pane's own row shows.
func sectionIcon(s Section) *ui.SVG {
	switch s {
	case SectionRules:
		return iconRule
	case SectionBreakpoints:
		return iconPause
	case SectionCertificate:
		return iconCert
	}
	return iconAll
}

// sectionCount is how many things the chosen pane holds.
func (a *App) sectionCount() int {
	switch a.Section {
	case SectionRules:
		return a.RuleCount
	case SectionBreakpoints:
		return a.PausedCount
	}
	return 0
}

// pane draws the chosen section.
//
// The traffic list is the window's subject; the other three are panels with their
// own headers, because a rules table and a certificate are not something a
// traffic column can show.
func (a *App) pane(c *ui.Context, p *Palette) {
	switch a.Section {
	case SectionRules:
		a.rulesPane(c, p)
	case SectionBreakpoints:
		a.breakpointsPane(c, p)
	case SectionCertificate:
		a.certificatePane(c, p)
	default:
		a.trafficList(c, p)
	}
}

// placeholderPane is the shape the panes that have no table yet take: a heading,
// what the pane is for, and where to change it from if the window cannot.
//
// It is honest about being a placeholder rather than an empty table: a table with
// no rows says the core has none, which would be a different thing.
func (a *App) placeholderPane(c *ui.Context, p *Palette, icon *ui.SVG, title, body string) {
	ui.Column(c).Fill().Background(p.Bg).Padding(tokenSpace5).Gap(tokenSpace3).Children(func() {
		ui.Row(c).Gap(tokenSpace2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).Width(16).Height(16).TextColor(p.TextMuted)
			ui.Text(c, title).Bold().FontSize(tokenFontTitle).TextColor(p.Text)
		})
		ui.Text(c, body).FontSize(tokenFontBody).TextColor(p.TextMuted).MaxWidth(460)
	})
}

// rulesPane lists the rules that rewrite traffic.
func (a *App) rulesPane(c *ui.Context, p *Palette) {
	a.placeholderPane(c, p, iconRule, "改写规则",
		fmt.Sprintf("共 %d 条。规则作用于之后进来的流量：请求进来时匹配一次，响应回来时再匹配一次。"+
			"在窗口里编辑规则还没有做完；现在用 /api/rules 增删改，改完这里会跟着变。", a.RuleCount))
}

// breakpointsPane lists the breakpoint rules, and what is waiting at them.
func (a *App) breakpointsPane(c *ui.Context, p *Palette) {
	a.placeholderPane(c, p, iconPause, "断点",
		fmt.Sprintf("现在有 %d 条消息停在断点上。断点会暂停流量等人决定，"+
			"和自动生效的规则不是一回事；暂停的横幅在列表上方，放行和中止在那里。", a.PausedCount))
}

// certificatePane shows the certificate to install on the device being debugged.
func (a *App) certificatePane(c *ui.Context, p *Palette) {
	state := "未安装"
	colour := p.Warning
	if a.CAInstalled {
		state, colour = "已安装", p.Success
	}
	ui.Column(c).Fill().Background(p.Bg).Padding(tokenSpace5).Gap(tokenSpace3).Children(func() {
		ui.Row(c).Gap(tokenSpace2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconCert).Width(16).Height(16).TextColor(p.TextMuted)
			ui.Text(c, "根证书").Bold().FontSize(tokenFontTitle).TextColor(p.Text)
			ui.Text(c, state).FontSize(tokenFontBody).TextColor(colour)
		})
		ui.Text(c, "要让 HTTPS 被解开，被调试的设备必须信任这个根证书。"+
			"下载后装进系统信任库，或者让 curl 用 --cacert 指过来。").
			FontSize(tokenFontBody).TextColor(p.TextMuted).MaxWidth(460)
		ui.Text(c, "curl -O http://127.0.0.1:8898/api/certificate?download=1").
			FontSize(tokenFontData).Font(monoFamily).TextColor(p.Text)
	})
}

// trafficList is the table of sessions, and the window's main subject.
func (a *App) trafficList(c *ui.Context, p *Palette) {
	ui.Column(c).Fill().Background(p.Bg).Children(func() {
		a.table(c, p)
	})
}

// filterField is the toolbar's filter box.
//
// It is in the toolbar rather than above the list because that is where a Mac
// user looks for a search field, and because it is the one control that narrows
// whatever the window is showing. The count beside it is what tells the user the
// filter did something.
func (a *App) filterField(c *ui.Context, p *Palette) {
	ui.Row(c).AlignItems(ui.Center).Gap(tokenSpace2).Children(func() {
		ui.Icon(c, iconFilter).Width(13).Height(13).TextColor(p.Dim)
		// The label names the box for a screen reader and for a test that clicks
		// it; the placeholder is only a hint about what it accepts.
		input := ui.TextInput(c, &a.Search).
			Label("过滤").Placeholder("过滤 URL、方法或状态码").Width(260)
		if input.Changed() {
			// The typed text narrows the list as it is typed; the table is built
			// after this, so it shows the new filter in this frame.
			a.Filter.Text = a.Search
			a.Invalidate()
		}
		if a.Search != "" {
			ui.Text(c, fmt.Sprintf("%d 条", len(a.Visible()))).FontSize(tokenFontSmall).
				Font(monoFamily).TextColor(p.Dim).SingleLine()
		}
	})
}

// columns are the traffic list's columns. Widths are fixed except the path,
// which takes the room the rest leave.
var columns = []ui.TableColumn{
	{Title: "#", ID: "id", Width: 40, Align: ui.End},
	{Title: "方法", ID: "method", Width: 62},
	{Title: "状态", ID: "status", Width: 48},
	{Title: "域名", ID: "host", Width: 128},
	// The path is a column with no natural width, but it is not the one that
	// takes the room left: a table gives the leftover width to its last column
	// that shares it, and if that were the path the numbers would be pushed to
	// the far right of a wide window. So the path has a width a path usually
	// needs, and the spacer after it is the column that absorbs the rest — the
	// numbers then sit just past the path, where a reader's eye already is.
	{Title: "路径", ID: "path", Width: 200, MinWidth: 140},
	// A numeric column is end-aligned, and a header in a narrow end-aligned
	// column is clipped at the edge it is aligned to; the titles are short
	// enough to fit, and the columns are wider than the values they hold.
	{Title: "大小", ID: "size", Width: 66, Align: ui.End},
	{Title: "耗时", ID: "time", Width: 68, Align: ui.End},
	// The spacer: a column with no title and no cells that takes the room the
	// others leave, so that the table fills its pane without stretching a
	// column that holds data.
	{Title: "", ID: "spacer"},
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
