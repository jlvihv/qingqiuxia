package ui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestBarRoomClearsTheWindowControls checks that the toolbar keeps out of the
// room the window's own controls take.
//
// macOS draws the traffic lights inside the content of a window with a hidden
// title bar, and the toolbar's first control was written at a fixed 46 DIPs from
// the left edge: the recording switch came out underneath the buttons, which
// also took its clicks. The room is the window's answer, so the bar is laid out
// from it — and a window in full screen, or one on a platform without an inset,
// reports no room and gets the plain margin.
func TestBarRoomClearsTheWindowControls(t *testing.T) {
	for _, tc := range []struct {
		name                string
		bar                 ui.TitleBar
		left, right, height float32
	}{
		{
			// What TitleBarHiddenInset with the traffic lights at (20, 20)
			// reports: the zoom button's right edge, and a band as tall as the
			// buttons plus their margin above and below.
			name: "macOS traffic lights", bar: ui.TitleBar{Height: 54, Left: 72, Bottom: 34},
			left: 82, right: tokenSpace4, height: 54,
		},
		{
			// Windows caption buttons on the right, in a 32-DIP band.
			name: "caption buttons on the right", bar: ui.TitleBar{Height: 32, Right: 138, Bottom: 32},
			left: tokenSpace4, right: 148, height: tokenHeightToolbar,
		},
		{
			// Full screen: no controls, no room.
			name: "no controls", bar: ui.TitleBar{},
			left: tokenSpace4, right: tokenSpace4, height: tokenHeightToolbar,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			left, right, height := barRoom(tc.bar)
			if left != tc.left || right != tc.right || height != tc.height {
				t.Errorf("barRoom(%+v) = %.0f, %.0f, %.0f; want %.0f, %.0f, %.0f",
					tc.bar, left, right, height, tc.left, tc.right, tc.height)
			}
		})
	}
}

// TestBarOverMaterialPaintsNothing checks the fill of a bar over the window's
// material.
//
// The window paints nothing where the material shows, so a bar must paint
// nothing too: the material itself is the bar's surface. An opaque fill hid it
// — the toolbar painted the theme's surface and the window read as a flat panel
// with the material visible only in the sidebar — and even a dark tint covers
// the thing that makes a Mac window read as a Mac window. A desktop with no
// material, or one that asks for opaque windows, keeps the theme's colour.
func TestBarOverMaterialPaintsNothing(t *testing.T) {
	for _, c := range []ui.Color{Dark.Surface, Dark.Raised} {
		if over := barFill(c, true); over.A != 0 {
			t.Errorf("barFill(%v, true) = %v, want it clear so the material shows", c, over)
		}
		if solid := barFill(c, false); solid != c {
			t.Errorf("barFill(%v, false) = %v, want it unchanged", c, solid)
		}
	}
}

// TestPaneOverMaterialKeepsItsOwnDarkness checks a content pane's fill.
//
// A pane may not be clear like a bar: a table of data is read over whatever the
// window is in front of, so it darkens the glass by most of its own colour. Test
// both ends: a fill too clear puts light text over the desktop, and one too
// opaque ends the glass at the pane's edge.
func TestPaneOverMaterialKeepsItsOwnDarkness(t *testing.T) {
	for _, c := range []ui.Color{Dark.Bg, Dark.Surface} {
		over := paneFill(c, true)
		if over.R != c.R || over.G != c.G || over.B != c.B {
			t.Errorf("paneFill(%v, true) = %v, want the same colour", c, over)
		}
		if got := float64(over.A) / 255; got < 0.55 || got > 0.75 {
			t.Errorf("paneFill(%v, true) alpha = %.2f, want between 0.55 and 0.75", c, got)
		}
		if solid := paneFill(c, false); solid != c {
			t.Errorf("paneFill(%v, false) = %v, want it unchanged", c, solid)
		}
	}
}

// TestCapsuleLiftsUnderThePointer checks a control's face.
//
// Over the material a control is a smaller pane of the same glass, so it gets
// *brighter* under the pointer rather than changing hue; below it there is
// nothing behind the control, so it gets the theme's raised faces.
func TestCapsuleLiftsUnderThePointer(t *testing.T) {
	p := &Dark
	rest := capsuleFill(p, true, false, false)
	hover := capsuleFill(p, true, true, false)
	pressed := capsuleFill(p, true, false, true)
	if !(rest.A < hover.A && hover.A < pressed.A) {
		t.Errorf("over glass alphas rest=%.2f hover=%.2f pressed=%.2f, want them growing",
			float64(rest.A)/255, float64(hover.A)/255, float64(pressed.A)/255)
	}
	if rest.A == 0 || float64(rest.A)/255 > 0.2 {
		t.Errorf("a capsule at rest is %.2f white, want a faint pane of glass", float64(rest.A)/255)
	}
	if got := capsuleFill(p, false, false, false); got != p.Raised {
		t.Errorf("capsule face without a material = %v, want the raised surface", got)
	}
	if got := capsuleBorder(p, false); got != p.Border {
		t.Errorf("capsule border without a material = %v, want the theme's", got)
	}
	if a := float64(capsuleBorder(p, true).A) / 255; a < 0.08 || a > 0.2 {
		t.Errorf("capsule border over glass alpha = %.2f, want a hairline", a)
	}
}

// TestWindowSmokeKeepsTheGlass checks the window's ground over the material.
//
// Clear would put the palette's light text on whatever is behind the window; opaque
// would end the glass at the window's edge. The smoke is neither: it darkens the
// material enough for the text and leaves most of it to the eye.
func TestWindowSmokeKeepsTheGlass(t *testing.T) {
	over := windowFill(Dark.Bg, true)
	if got := float64(over.A) / 255; got < 0.25 || got > 0.6 {
		t.Errorf("windowFill over glass alpha = %.2f, want between 0.25 and 0.6", got)
	}
	if over.R != Dark.Bg.R || over.G != Dark.Bg.G || over.B != Dark.Bg.B {
		t.Errorf("windowFill over glass = %v, want the palette's background", over)
	}
	if got := windowFill(Dark.Bg, false); got != Dark.Bg {
		t.Errorf("windowFill without a material = %v, want it unchanged", got)
	}
}
