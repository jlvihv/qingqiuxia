package ui

import "github.com/egoist/mygo/ui"

// content lays out the window below the toolbar and above the status bar: the
// sidebar, the traffic list and the detail panel, with dividers the user drags.
//
// The detail panel's width is what the user sets, so the split is given the
// main area's width and the panel takes the rest. MyGo's split sizes its first
// pane, and the sidebar is the first of the two here.
func (a *App) content(c *ui.Context, p *Palette) {
	// The two splits carry the sizes the user drags: the sidebar's, and the
	// detail panel's kept as the remainder of the window.
	ui.Split(c, &a.SidebarWidth, func() {
		a.sidebar(c, p)
	}, func() {
		a.mainAndDetail(c, p)
	}).Grow(1).FillWidth()
}

// mainAndDetail puts the traffic list and the detail panel side by side.
func (a *App) mainAndDetail(c *ui.Context, p *Palette) {
	// The main area keeps its width in MainWidth; ui.Split sizes the first
	// pane, which is the list.
	ui.Split(c, &a.MainWidth, func() {
		a.trafficList(c, p)
	}, func() {
		a.detail(c, p)
	}).Fill()
}
