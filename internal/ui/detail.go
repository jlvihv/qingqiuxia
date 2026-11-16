package ui

import (
	"context"
	"errors"
	"fmt"

	"github.com/egoist/mygo/ui"
)

// detailTabs are the detail panel's sections.
var detailTabs = []string{"概览", "请求", "响应", "说明", "时序"}

// detail is the right panel: everything about the chosen session.
//
// It is a fixed side panel rather than a second window, because the list stays
// the thing the user works from, and a row is judged by what the panel says
// beside it.
func (a *App) detail(c *ui.Context, p *Palette) {
	ui.Column(c).Fill().Background(p.Surface).Children(func() {
		a.tabBar(c, p)
		sess, ok := a.SelectedSession()
		if !ok {
			ui.Column(c).Fill().Center().Children(func() {
				ui.Text(c, "选一条流量看详情").FontSize(tokenFontBody).TextColor(p.Dim)
			})
			return
		}
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(tokenSpace4, 14).Gap(0).Children(func() {
				switch a.Detail {
				case 0:
					a.overviewPane(c, p, &sess)
				case 1:
					a.messagePane(c, p, &sess, "请求")
				case 2:
					a.messagePane(c, p, &sess, "响应")
				case 3:
					a.notesPane(c, p, &sess)
				case 4:
					a.timingPane(c, p, &sess)
				}
			})
		})
	})
}

// tabBar is the detail panel's tabs, drawn here rather than with ui.Tabs so
// that the line under the chosen tab is the window's accent: the framework's
// tabs take their line from the theme's Accent, which is the subdued selection
// colour, and a dim line under a chosen tab reads as a disabled one.
func (a *App) tabBar(c *ui.Context, p *Palette) {
	tabs := ui.TabsBase(c, &a.Detail, len(detailTabs))
	ui.Row(c).FillWidth().Padding(tokenSpace3, tokenSpace3, 0, 0).
		Border(0, p.Border).Children(func() {
		tabs.List.Gap(tokenSpace1).Children(func() {
			for i, label := range detailTabs {
				tab := tabs.Tab(i).Padding(tokenSpace2, tokenSpace3)
				on := i == a.Detail
				if on {
					tab.Border(0, p.Accent)
				}
				tab.Children(func() {
					e := ui.Text(c, label).FontSize(tokenFontBody)
					if on {
						e.TextColor(p.Text).Bold()
					} else {
						e.TextColor(p.TextMuted)
					}
				})
			}
		})
	})
}

// urlBar is the request line, which every pane starts with: the one thing a
// reader checks first.
func (a *App) urlBar(c *ui.Context, p *Palette, sess *Session) {
	ui.Row(c).FillWidth().Padding(7, tokenSpace3).Gap(8).Radius(tokenRadiusCard).
		Background(p.Raised).Border(1, p.Border).Children(func() {
		ui.Text(c, sess.Method).FontSize(tokenFontBody).Bold().Font(monoFamily).
			TextColor(p.MethodColor(sess.Method))
		ui.Text(c, sess.URL).FontSize(tokenFontBody).Font(monoFamily).
			TextColor(p.Text).Grow(1)
		if sess.Done() {
			ui.Text(c, fmt.Sprintf("%d %s", sess.Status, statusText(sess.Status))).
				FontSize(11.5).Font(monoFamily).TextColor(p.StatusColor(sess.Status))
		}
	})
}

// statusText is the reason phrase for a status, as the panel shows beside it.
func statusText(status int) string {
	switch {
	case status == 0:
		return ""
	case status < 300:
		return "OK"
	case status == 301:
		return "Moved Permanently"
	case status == 302:
		return "Found"
	case status == 304:
		return "Not Modified"
	case status == 400:
		return "Bad Request"
	case status == 401:
		return "Unauthorized"
	case status == 403:
		return "Forbidden"
	case status == 404:
		return "Not Found"
	case status == 500:
		return "Internal Server Error"
	case status == 502:
		return "Bad Gateway"
	case status == 503:
		return "Service Unavailable"
	}
	return ""
}

// section labels a group inside a pane.
func (a *App) section(c *ui.Context, p *Palette, title string, first bool) {
	e := ui.Text(c, title).FontSize(tokenFontLabel).Bold().TextColor(p.Dim)
	if first {
		e.Padding(0, 10, tokenSpace2, 0)
	} else {
		e.Padding(0, tokenSpace5, tokenSpace2, 0)
	}
}

// overviewPane is the summary: timing, size and the headers that explain them.
func (a *App) overviewPane(c *ui.Context, p *Palette, sess *Session) {
	a.urlBar(c, p, sess)

	a.section(c, p, "时间与大小", false)
	a.kv(c, p, "总耗时", formatMillis(sess.DurationMS))
	a.kv(c, p, "首字节", formatMillis(sess.TTFBMS))
	a.kv(c, p, "DNS", formatMillis(sess.DNSMS))
	a.kv(c, p, "连接", formatMillis(sess.ConnectMS))
	a.kv(c, p, "TLS", formatMillis(sess.TLSMS))
	a.kv(c, p, "大小", fmt.Sprintf("%s ↑ %s",
		formatBytes(sess.BytesOut), formatBytes(sess.BytesIn)))
	a.kv(c, p, "协议", sess.Proto)

	if sess.Error != "" {
		a.section(c, p, "错误", false)
		ui.Text(c, sess.Error).FontSize(tokenFontData).Font(monoFamily).
			TextColor(p.Danger).Padding(0, 10, tokenSpace1, 0)
	}
	if len(sess.Notes) > 0 {
		a.section(c, p, "这个请求为什么长这样", false)
		for _, n := range sess.Notes {
			a.note(c, p, n)
		}
	}
}

// kv is one label and value pair of the summary.
func (a *App) kv(c *ui.Context, p *Palette, label, value string) {
	ui.Row(c).FillWidth().Padding(0, 10, 3, 0).Gap(10).Children(func() {
		ui.Text(c, label).FontSize(tokenFontBody).TextColor(p.TextMuted).Width(72)
		ui.Text(c, value).FontSize(tokenFontData).Font(monoFamily).
			TextColor(p.Text).Grow(1).SingleLine()
	})
}

// note explains one thing the pipeline did, which is the answer to "why does
// this request look like this".
func (a *App) note(c *ui.Context, p *Palette, n Note) {
	colour := p.Warning
	switch n.Kind {
	case "proxy":
		colour = p.Info
	case "breakpoint":
		colour = p.Danger
	}
	ui.Row(c).FillWidth().Padding(6, 8).Gap(8).Margin(0, 10, tokenSpace1, 0).
		Background(p.Raised).Children(func() {
		src := n.Rule
		if src == "" {
			src = n.Kind
		}
		ui.Text(c, src).FontSize(tokenFontSmall).Font(monoFamily).
			TextColor(colour).Width(90).SingleLine()
		ui.Text(c, n.Text).FontSize(tokenFontData).TextColor(p.Text).Grow(1)
	})
}

// messagePane shows a request's or a response's headers and body.
func (a *App) messagePane(c *ui.Context, p *Palette, sess *Session, side string) {
	a.urlBar(c, p, sess)

	// The bodies are fetched for the chosen session, and only while a pane that
	// shows them is open: the list carries none, because thousands of rows with
	// their bodies is most of a gigabyte for detail one click asks for.
	d, err := a.bodiesFor(sess.ID)
	if err != nil {
		a.bodyNotice(c, p, "取不到内容："+err.Error())
		return
	}
	if side == "请求" {
		a.bodyPane(c, p, &d.Request, "请求")
		return
	}
	a.bodyPane(c, p, &d.Response, "响应")
}

// bodiesFor returns the chosen session's bodies, fetching them when the pane
// first shows it.
//
// It asks once per session: a frame runs many times a second, and a request per
// frame would be a request per frame. A failure is remembered too — a session the
// core has already dropped should not be retried every frame — but it is
// remembered as a failure. Returning nil for it on later frames made the pane say
// a session had no content, when what had happened is that its body was never
// read.
func (a *App) bodiesFor(id uint64) (Detail, error) {
	if a.detailID == id {
		if a.detailErr != "" {
			return Detail{}, errors.New(a.detailErr)
		}
		return a.bodies, nil
	}
	a.detailID = id
	a.detailKey = id
	a.bodies = Detail{}
	a.detailErr = ""
	if a.backend == nil {
		return Detail{}, nil
	}
	d, err := a.backend.Detail(context.Background(), id)
	if err != nil {
		a.detailErr = err.Error()
		return Detail{}, err
	}
	a.bodies = d
	return d, nil
}

// notesPane is the full list of what happened to a session.
func (a *App) notesPane(c *ui.Context, p *Palette, sess *Session) {
	a.urlBar(c, p, sess)
	if len(sess.Notes) == 0 {
		a.section(c, p, "说明", false)
		ui.Text(c, "没有规则或断点动过这条流量。").FontSize(tokenFontData).
			TextColor(p.TextMuted).Padding(0, 10, tokenSpace1, 0)
		return
	}
	a.section(c, p, fmt.Sprintf("共 %d 条", len(sess.Notes)), false)
	for _, n := range sess.Notes {
		a.note(c, p, n)
	}
}

// timingPane draws where the time went, as a waterfall: a bar per phase,
// scaled to the whole exchange so the phases compare at a glance.
func (a *App) timingPane(c *ui.Context, p *Palette, sess *Session) {
	a.urlBar(c, p, sess)
	a.section(c, p, "时序", false)

	total := sess.DurationMS
	if total <= 0 {
		total = 1
	}
	phases := []struct {
		label  string
		ms     float64
		colour ui.Color
	}{
		{"DNS", sess.DNSMS, p.Info},
		{"连接", sess.ConnectMS, p.Success},
		{"TLS", sess.TLSMS, p.Warning},
		{"等待", sess.TTFBMS - sess.DNSMS - sess.ConnectMS - sess.TLSMS, p.Accent},
		{"下载", total - sess.TTFBMS, p.TextMuted},
	}
	for _, ph := range phases {
		if ph.ms < 0 {
			ph.ms = 0
		}
		// The bar is the phase's share of the whole exchange.
		frac := float32(ph.ms / total)
		if frac > 1 {
			frac = 1
		}
		ui.Row(c).FillWidth().Padding(0, 10, tokenSpace1, 0).Gap(8).Children(func() {
			ui.Text(c, ph.label).FontSize(tokenFontSmall).TextColor(p.TextMuted).Width(40)
			ui.Row(c).Grow(1).Height(7).Radius(tokenRadiusBadge).
				Background(p.Raised).Children(func() {
				ui.Row(c).Fill().Radius(tokenRadiusBadge).Background(ph.colour)
			})
			ui.Text(c, formatMillis(ph.ms)).FontSize(tokenFontLabel).Font(monoFamily).
				TextColor(p.TextMuted).Width(54)
		})
	}
}

// statusBar is the footer: the counts that tell a user whether to trust what
// they see, including what the core dropped.
func (a *App) statusBar(c *ui.Context, p *Palette) {
	total, dropped := a.Store.Counts()
	ui.Row(c).FillWidth().Height(tokenHeightStatus).PaddingX(tokenSpace4).Gap(tokenSpace4).
		Background(p.Raised).Children(func() {
		a.stat(c, p, "会话", fmt.Sprint(total), p.TextMuted)
		a.stat(c, p, "丢弃", fmt.Sprint(dropped), p.Warning)
		ui.Row(c).Grow(1)
		a.stat(c, p, "核心", fmt.Sprintf("%d MB · %d 线程",
			a.CoreMemoryMB, a.CoreThreads), p.Dim)
		a.stat(c, p, "api", a.Version, p.Dim)
	})
}

// stat is one pair of label and value in the status bar.
func (a *App) stat(c *ui.Context, p *Palette, label, value string, colour ui.Color) {
	ui.Row(c).Gap(tokenSpace1).Children(func() {
		ui.Text(c, label).FontSize(tokenFontSmall).TextColor(p.Dim)
		ui.Text(c, value).FontSize(tokenFontSmall).Font(monoFamily).TextColor(colour)
	})
}
