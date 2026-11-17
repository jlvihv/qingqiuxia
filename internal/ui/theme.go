// Package ui is the desktop window: a native MyGo interface over the core's
// control API.
//
// The window never links the proxy. It is one client of the API, as a script
// or a browser would be, so the core keeps working with no window attached and
// the interface can be replaced without touching it.
package ui

import "github.com/egoist/mygo/ui"

// The design tokens.
//
// Everything visible in this window comes from one of these, and no builder
// writes a colour, a radius or a spacing by hand. That is what keeps the
// interface coherent: a new panel cannot invent a slightly different grey, and
// retuning the whole look is a change to this file.
const (
	// Colour scale. Backgrounds step in brightness only; borders are the last
	// resort, for what brightness alone cannot separate.
	tokenBg      = 0x0e1014 // the window behind the panels
	tokenSurface = 0x15181e // a panel
	tokenRaised  = 0x1b1f27 // a card, a control, an input
	tokenHover   = 0x22262f // a control under the pointer
	tokenPressed = 0x2a2f3a // a control pressed
	// A hairline is dim on purpose: on a near-black panel, a border at the
	// brightness of a control's face reads as a bright rule and pulls the eye
	// away from the data. It separates only when nothing else can.
	tokenBorder    = 0x14171d
	tokenBorderStr = 0x2f3540 // a hairline that must be seen, as a focus edge
	tokenText      = 0xe6e9ef // primary text
	tokenTextMuted = 0x868e9c // secondary text, labels
	tokenDim       = 0x5b636f // a section label, a count
	tokenAccent    = 0x4c8dff
	tokenSuccess   = 0x3fb950
	tokenWarning   = 0xd29922
	tokenInfo      = 0x58a6ff
	tokenDanger    = 0xf85149

	// Radii. A 4 is a badge, a 5 a control, a 6 a card.
	tokenRadiusBadge   float32 = 3
	tokenRadiusControl float32 = 5
	tokenRadiusCard    float32 = 6
	// tokenRadiusCapsule is a toolbar control's radius: half of its 28-DIP
	// height, which makes it the capsule macOS 26 draws in a toolbar. The
	// older radius is kept for the controls in a panel, where a capsule beside
	// a rounded input reads as two styles on one surface.
	tokenRadiusCapsule float32 = 14

	// Spacing steps, in DIPs. The window uses these and nothing in between.
	tokenSpace1 float32 = 4
	tokenSpace2 float32 = 6
	tokenSpace3 float32 = 10
	tokenSpace4 float32 = 14
	tokenSpace5 float32 = 20

	// Type sizes. Data is always monospaced, chrome never is.
	tokenFontMicro float32 = 9.5  // a badge
	tokenFontLabel float32 = 10.5 // a section label
	tokenFontSmall float32 = 11
	tokenFontData  float32 = 11.5 // a table cell, a header line
	tokenFontBody  float32 = 12
	tokenFontUI    float32 = 12.5 // sidebar, tabs
	tokenFontTitle float32 = 13

	// Row and bar heights, so that a list and a panel line up.
	//
	// The row is 28, not 24: a capture tool's rows carry five columns of data and
	// a badge, and at 24 the status code and the duration touch each other at the
	// sizes macOS draws. 28 is what Finder's list and Proxyman's table use.
	tokenHeightToolbar float32 = 52 // macOS unified toolbars are 52 with a hidden title bar
	tokenHeightBar     float32 = 36
	tokenHeightStatus  float32 = 26
	tokenHeightRow     float32 = 28
)

// monoFamily is the monospaced family every datum uses, without exception: it
// is what keeps digits aligned down a column, so that an anomalous duration is
// visible while scanning rather than on inspection.
//
// It names the system's own faces rather than the generic "monospace": that
// keyword resolves to Courier on macOS, which is both ugly and wider than the
// data columns were measured for. SF Mono ships with macOS, Menlo with every Mac,
// and the rest cover the other two platforms; the generic family is last so a
// machine without any of them still gets a monospaced fallback.
const monoFamily = "SF Mono, SFMono-Regular, Menlo, Consolas, DejaVu Sans Mono, monospace"

// Palette is the interface's colours, resolved from the tokens once.
//
// The rule it follows: colour carries meaning, never decoration. Backgrounds
// step in brightness, an accent appears only where something is chosen or
// focused, and the hues for status and method are the ones every capture tool
// uses, so they cost the user nothing to learn.
type Palette struct {
	Bg, Surface, Raised, Hover, Pressed ui.Color
	// Border separates what brightness alone cannot; BorderStrong marks a
	// focused control's edge.
	Border, BorderStrong ui.Color
	// Text, TextMuted and Dim are the three text levels.
	Text, TextMuted, Dim ui.Color
	Accent, AccentText   ui.Color
	// Selection fills a chosen row: a subdued accent, dark enough that the
	// row's own colours stay readable on it.
	Selection                      ui.Color
	Success, Warning, Info, Danger ui.Color
}

// Dark is the palette the design was drawn in: near-black, low chrome, and
// text that stays legible on a monitor at arm's length.
var Dark = Palette{
	Bg:           ui.Hex("#0e1014"),
	Surface:      ui.Hex("#15181e"),
	Raised:       ui.Hex("#1b1f27"),
	Hover:        ui.Hex("#22262f"),
	Pressed:      ui.Hex("#2a2f3a"),
	Border:       ui.Hex("#14171d"),
	BorderStrong: ui.Hex("#2f3540"),
	Text:         ui.Hex("#e6e9ef"),
	TextMuted:    ui.Hex("#868e9c"),
	Dim:          ui.Hex("#5b636f"),
	Accent:       ui.Hex("#4c8dff"),
	AccentText:   ui.Hex("#ffffff"),
	Selection:    ui.Hex("#1d3a6b"),
	Success:      ui.Hex("#3fb950"),
	Warning:      ui.Hex("#d29922"),
	Info:         ui.Hex("#58a6ff"),
	Danger:       ui.Hex("#f85149"),
}

// The fills over the window's material.
//
// macOS draws Liquid Glass behind a window (mygo.VibrancyGlass), and it shows
// wherever the view paints nothing. Three rules follow, and every surface obeys
// one of them:
//
//   - A bar paints nothing at all (barFill). The material is the bar's surface;
//     a tint of its own would hide the thing it was asked for.
//   - A control on a bar is a smaller pane of the same glass: a little white,
//     more under the pointer, with a hairline (capsuleFill, capsuleBorder).
//   - A content pane darkens the glass (paneFill) so that a table of data stays
//     readable over whatever the window is in front of, without becoming an
//     opaque slab that ends the glass at its edge.
//
// Without a material — another platform, or Reduce Transparency — each returns
// the theme's own surface, and the window is the flat one it always was.

// contentAlpha is how much of a content pane's own colour is kept over the
// material. High enough that light text on it clears the glass under it on a
// bright desktop, low enough that the pane still reads as a pane of it.
const contentAlpha float32 = 0.58

// smokeAlpha is how much of the window's own colour sits over the material
// before anything is drawn on it.
//
// The interface has one palette, and it is dark. On a window that shows the
// material, leaving the whole ground clear makes light text sit on whatever
// happens to be behind the window — an editor with a white page, say — and the
// muted levels stop being readable. A smoke of the window's own colour keeps
// the glass, which is still more than half of what the eye sees, and gives the
// text the dark ground the palette was drawn for.
const smokeAlpha float32 = 0.40

// panelAlpha is how opaque a floating panel over the material is: a popover, a
// menu hung under a button. Higher than a content pane's: the system draws the
// popover's material over whatever is behind the window, and over a bright
// desktop that material is bright enough to swallow light text. Most of the
// window's own colour leaves the frost of the material at the edges and gives
// the text its ground.
const panelAlpha float32 = 0.80

// panelFill is a floating panel's ground over the material: its colour,
// translucent, or the colour itself where there is no material.
func panelFill(c ui.Color, vibrancy bool) ui.Color {
	if !vibrancy {
		return c
	}
	return ui.RGBA(c.R, c.G, c.B, panelAlpha)
}

// windowFill is the window's ground: its colour without a material, and that
// colour as a smoke over the material with one.
func windowFill(c ui.Color, vibrancy bool) ui.Color {
	if !vibrancy {
		return c
	}
	return ui.RGBA(c.R, c.G, c.B, smokeAlpha)
}

// barFill is a bar's background: its colour without a material, and nothing
// with one.
func barFill(c ui.Color, vibrancy bool) ui.Color {
	if !vibrancy {
		return c
	}
	return ui.Transparent
}

// paneFill is a content pane's background: its colour, translucent over the
// material.
func paneFill(c ui.Color, vibrancy bool) ui.Color {
	if !vibrancy {
		return c
	}
	return ui.RGBA(c.R, c.G, c.B, contentAlpha)
}

// raisedFill is what reads as raised over the material: white at 12%, the fill
// of the chosen segment of a switch and of a pill that stands out from the bar.
// The theme's surface cannot do that on its own: over glass it lands as another
// opaque chip rather than as a smaller pane of the same light.
func raisedFill(p *Palette, vibrancy bool) ui.Color {
	if !vibrancy {
		return p.Pressed
	}
	return ui.RGBA(0xff, 0xff, 0xff, 0.12)
}

// capsuleFill is the face of a control on a bar: glass at rest, a little
// brighter under the pointer and brighter still while pressed. Without a
// material it is the theme's control face.
func capsuleFill(p *Palette, vibrancy, hovered, pressed bool) ui.Color {
	if !vibrancy {
		switch {
		case pressed:
			return p.Pressed
		case hovered:
			return p.Hover
		}
		return p.Raised
	}
	switch {
	case pressed:
		return ui.RGBA(0xff, 0xff, 0xff, 0.22)
	case hovered:
		return ui.RGBA(0xff, 0xff, 0xff, 0.16)
	}
	return ui.RGBA(0xff, 0xff, 0xff, 0.10)
}

// capsuleBorder is the hairline a capsule of glass is outlined with: the light
// that catches its edge, not a border dark enough to read as a rule.
func capsuleBorder(p *Palette, vibrancy bool) ui.Color {
	if !vibrancy {
		return p.Border
	}
	return ui.RGBA(0xff, 0xff, 0xff, 0.14)
}

// sidebarFill is what the source list paints behind its items.
//
// Nothing, when the window shows a material: macOS puts the sidebar material
// behind a view that draws no background, and a fill — even a dark one — covers
// the blur it was asked for. Where there is no material this transparents onto
// the window's own background, which is the same colour the panel beside it
// sits on.
func sidebarFill() ui.Color { return ui.RGBA(0, 0, 0, 0) }

// StatusColor is the colour a status code is written in: the family of the
// code, which is what a reader scans for.
func (p *Palette) StatusColor(status int) ui.Color {
	switch {
	case status == 0:
		return p.TextMuted
	case status < 300:
		return p.Success
	case status < 400:
		return p.Info
	case status < 500:
		return p.Warning
	}
	return p.Danger
}

// MethodColor is the colour a method is written in.
func (p *Palette) MethodColor(method string) ui.Color {
	switch method {
	case "GET":
		return p.Success
	case "POST":
		return p.Warning
	case "PUT", "PATCH":
		return p.Info
	case "DELETE":
		return p.Danger
	}
	return p.TextMuted
}

// Theme returns MyGo's theme built from the same tokens, so that the framework's
// own widgets — an input's face, a scroll bar, a focus ring, a tab's underline —
// sit in the same palette as the panels around them.
//
// Every field MyGo draws with is set: a field left zero would make a control
// fall back to the framework's default grey, which is how an input ends up
// lighter than the panel it sits in.
func (p *Palette) Theme() ui.Theme {
	return ui.Theme{
		Dark: true,
		// The window and its panels share one background: the panels read as
		// regions of the same surface, not as cards floating on it.
		Background:     p.Bg,
		Surface:        p.Raised,
		SurfaceHover:   p.Hover,
		SurfacePressed: p.Pressed,
		Border:         p.Border,
		Text:           p.Text,
		TextMuted:      p.TextMuted,
		// The framework fills a chosen row and a primary button with Accent. A
		// row in full-strength blue is tiring in a log read for hours, so the
		// window's accent is the subdued selection colour; a source list speaks
		// for itself with the brighter one (see App.sidebar), because its icons
		// are navigation and have to be read at a glance.
		Accent:         p.Selection,
		AccentHover:    ui.Hex("#2a4c85"),
		AccentPressed:  ui.Hex("#16294d"),
		AccentText:     p.Text,
		InactiveAccent: p.BorderStrong,
		Danger:         p.Danger,
		Warning:        p.Warning,
		Success:        p.Success,
		// A chosen row is filled with a subdued accent, not the accent itself:
		// a table with one row in full blue is tiring to read, and the row's
		// text has to stay legible on it.
		Selection:       p.Selection,
		Focus:           ui.RGBA(0x4c, 0x8d, 0xff, 0.6),
		FocusRingWidth:  2,
		FocusRingOffset: 1,
		// 滚动条要能被找到，但不能比内容还显眼。近黑背景上，白 12% 的 thumb 是
		// #2a2a2a 一类的亮度，跟面板几乎分不开，却又在静止时一直跳出来；降到 8%
		// 之后它只在需要时被注意到，鼠标移上去才亮起来。轨道不画：一条贯穿整列
		// 的浅色竖条，是列表里最抢视线的东西。
		Scrollbar:      ui.RGBA(0xff, 0xff, 0xff, 0.08),
		ScrollbarWidth: 6,
		ScrollbarStyle: ui.ScrollbarStyle{
			Thumb:  ui.RGBA(0xff, 0xff, 0xff, 0.08),
			Hover:  ui.RGBA(0xff, 0xff, 0xff, 0.16),
			Active: ui.RGBA(0xff, 0xff, 0xff, 0.24),
			Track:  ui.RGBA(0, 0, 0, 0),
		},
		// The radii and spacing the framework's widgets use, from the same
		// tokens as the panels, so a button and a card share their corners.
		Radius:   tokenRadiusControl,
		Spacing:  tokenSpace1,
		FontSize: tokenFontTitle,
		Font:     "",
	}
}
