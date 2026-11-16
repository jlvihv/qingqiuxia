package ui

import "github.com/egoist/mygo/ui"

// content lays out the window below the toolbar and above the status bar: the
// source list, the chosen pane, and the detail panel for whatever is chosen in
// it.
//
// The two splits carry the sizes the user drags: the source list's, and the
// pane's. MyGo's split sizes its first pane, so the detail panel takes the rest
// of the width — which is right, because a detail panel has a width a user sets
// once and a list wants whatever is left.
func (a *App) content(c *ui.Context, p *Palette) {
	ui.Split(c, &a.SidebarWidth, func() {
		a.sidebar(c, p)
	}, func() {
		a.paneAndDetail(c, p)
	}).Grow(1).FillWidth()
}

// paneAndDetail puts the chosen pane and the detail panel side by side.
//
// The detail panel is an inspector: it exists to explain one chosen exchange, so
// it appears when there is one and not before. Showing it empty cost a third of
// the window's width to say "选一条流量看详情", which the list itself already
// implies — a capture tool opens on the traffic, not on a blank side panel.
//
// The other panes have nothing for it to inspect at all, so they take the whole
// width.
func (a *App) paneAndDetail(c *ui.Context, p *Palette) {
	if a.Section != SectionTraffic || !a.hasSelection() {
		a.pane(c, p)
		return
	}
	ui.Split(c, &a.MainWidth, func() {
		a.pane(c, p)
	}, func() {
		a.detail(c, p)
	}).Fill()
}

// hasSelection reports whether a row is chosen, and so whether there is anything
// for the detail panel to show.
func (a *App) hasSelection() bool {
	_, ok := a.SelectedSession()
	return ok
}
