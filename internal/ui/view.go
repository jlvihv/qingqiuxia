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
	// Detail is the tab the detail panel shows.
	Detail int
	// SidebarWidth and MainWidth are the two splits' first panes, which the
	// dividers adjust: the sidebar, and the traffic list.
	SidebarWidth, MainWidth float32

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

	// visible caches the indices the filter lets through, so that a frame
	// does not re-filter a list of thousands twice.
	visible      []int
	visibleDirty bool
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

// Down reports why the core is unreachable, empty when it is reachable.
func (a *App) Down() string { return a.down }

// Invalidate tells the app its filter or data changed.
func (a *App) Invalidate() { a.visibleDirty = true }

// Visible returns the rows the filter lets through, as indices into the store.
func (a *App) Visible() []int {
	if !a.visibleDirty && a.visible != nil {
		return a.visible
	}
	all := a.Store.Snapshot()
	out := make([]int, 0, len(all))
	for i := range all {
		if a.Filter.Match(&all[i]) {
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

// View builds the window's interface for a frame.
func (a *App) View(c *ui.Context) {
	// The theme is set on the context, not passed to the window: it is what
	// every control in the frame reads its colours and metrics from, and a
	// field left at its default would put a framework grey next to our panels.
	t := Dark.Theme()
	c.SetTheme(&t)

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
		a.chip(c, p, "代理", a.ProxyAddr)
		ca := "未安装"
		if a.CAInstalled {
			ca = "已安装"
		}
		a.chip(c, p, "CA", ca)
		ui.Text(c, "流量").TextColor(p.Text).FontSize(tokenFontUI)
		ui.Text(c, fmt.Sprintf("规则 %d", 0)).TextColor(p.TextMuted).FontSize(tokenFontUI)
		if a.Paused != nil {
			ui.Text(c, "断点 1").TextColor(p.Danger).FontSize(tokenFontUI)
		}
	})
}

// recordingDot says whether the core is capturing, in colour and in words.
func (a *App) recordingDot(c *ui.Context, p *Palette) {
	colour := p.Danger
	label := "录制中"
	if a.down != "" {
		// A core that is not reachable must not keep a red light on: a stale
		// "recording" tells the user the opposite of what is happening.
		colour = p.Dim
		label = "核心未连接"
	} else if !a.Recording {
		colour = p.Dim
		label = "已停止"
	}
	ui.Row(c).Gap(6).Children(func() {
		ui.Row(c).Width(8).Height(8).Radius(4).Background(colour)
		ui.Text(c, label).Bold().FontSize(tokenFontTitle).TextColor(p.Text)
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
		ui.Button(c, "中止")
		ui.PrimaryButton(c, "放行")
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
		a.sidebarItem(c, p, "全部流量", a.Store.Len(), true)
		a.sidebarSection(c, p, "域名")
		for _, h := range a.Store.Hosts() {
			a.sidebarItem(c, p, h.Host, h.Count, false)
		}
	})
}

// sidebarSection labels a group of sidebar items.
func (a *App) sidebarSection(c *ui.Context, p *Palette, title string) {
	ui.Text(c, title).FontSize(tokenFontLabel).Bold().
		TextColor(p.Dim).Padding(tokenSpace4, 10, tokenSpace1, 0)
}

// sidebarItem is one clickable row in the sidebar.
func (a *App) sidebarItem(c *ui.Context, p *Palette, label string, count int, active bool) {
	e := ui.Row(c).FillWidth().Height(tokenHeightRow).PaddingX(tokenSpace4).Gap(tokenSpace3)
	if active {
		e.Background(p.Hover)
	}
	e.Children(func() {
		ui.Text(c, label).FontSize(tokenFontUI).SingleLine().TextColor(p.TextMuted)
		ui.Row(c).Grow(1)
		ui.Text(c, fmt.Sprint(count)).FontSize(tokenFontSmall).Font(monoFamily).TextColor(p.Dim)
	})
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
		ui.TextInput(c, &a.Search).Placeholder("过滤 URL、方法或状态码").Grow(1)
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
		if sess, ok := a.Store.At(rows[row]); ok {
			return sess.ID
		}
		return row
	}
	ui.Table(c, a.list, columns, len(rows), func(row, col int) {
		sess, ok := a.Store.At(rows[row])
		if !ok {
			return
		}
		// A chosen row keeps its colours: the selection is a subdued fill
		// behind the text, not a different palette. A status code stays red
		// when chosen, which is the whole point of colouring it.
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

var _ = ui.Column
