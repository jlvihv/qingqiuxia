package ui

import "github.com/egoist/mygo/ui"

// The window's icons.
//
// They are drawn as SVG paths rather than shipped as files, because there are a
// handful and they are small: an icon that is a string cannot get out of step
// with the code that shows it. Each is on a 16×16 grid with a 1.5 stroke, the
// proportions macOS toolbars and sidebars use, and every one is a line drawing
// so that a single colour — the theme's text colour — is enough.
//
// Parse them once at start-up: an SVG parsed in a view is parsed on every frame.
var (
	iconAll     = icon("M2.5 4.5h11M2.5 8h11M2.5 11.5h7", "")
	iconHost    = icon("M8 2.5a5.5 5.5 0 1 0 0 11 5.5 5.5 0 0 0 0-11ZM2.5 8h11M8 2.5c1.6 1.7 2.4 3.6 2.4 5.5S9.6 12.3 8 13.5c-1.6-1.2-2.4-3.1-2.4-5.5S6.4 4.2 8 2.5Z", "")
	iconRule    = icon("M2.5 12.5 6 3.5M8.5 12.5 12 3.5M4 8h8", "")
	iconPause   = icon("M6 3.5v9M10 3.5v9", "")
	iconCert    = icon("M8 2.5 3.5 5v4c0 2.8 1.9 4.6 4.5 5.5 2.6-.9 4.5-2.7 4.5-5.5V5L8 2.5Z", "")
	iconProxy   = icon("M6 2.5v3M10 2.5v3M4.5 5.5h7v2a3.5 3.5 0 0 1-7 0v-2ZM8 11v2.5", "")
	iconFilter  = icon("M2.5 3.5h11l-4.2 5.2v4.1l-2.6 1.3V8.7L2.5 3.5Z", "")
	iconChevron = icon("M6 4l4 4-4 4", "")
	iconRecord  = icon("M8 4.5a3.5 3.5 0 1 0 0 7 3.5 3.5 0 0 0 0-7Z", "fill")
)

// icon wraps SVG path data in a minimal 16×16 document.
//
// fill draws the path as a filled shape instead of a stroked line, which is what
// a record dot and a few other glyphs want.
func icon(d, fill string) *ui.SVG {
	attr := `fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"`
	if fill == "fill" {
		attr = `fill="currentColor" stroke="none"`
	}
	doc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" width="16" height="16">` +
		`<path d="` + d + `" ` + attr + `/></svg>`
	return ui.MustParseSVG([]byte(doc))
}
