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
func (a *App) paneAndDetail(c *ui.Context, p *Palette) {
	ui.Split(c, &a.MainWidth, func() {
		a.pane(c, p)
	}, func() {
		a.detail(c, p)
	}).Fill()
}
