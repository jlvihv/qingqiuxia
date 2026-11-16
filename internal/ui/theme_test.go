package ui

import (
	"math"
	"testing"
)

// TestPaletteAlphaIsOpaqueWhereItSays checks the theme's translucent colours,
// which are the ones a mistake is invisible in.
//
// ui.RGBA takes alpha as a float from 0 to 1 and clamps anything above 1 to
// opaque. Every one of these was written with a byte — 0x14, 0x99 — which is far
// above 1, so each one drew fully opaque while its comment said "8%", "60%". A
// scrollbar at full white over a near-black panel is a bright bar down every
// list, and a pause banner that was meant to be a tint painted nothing at all.
func TestPaletteAlphaIsOpaqueWhereItSays(t *testing.T) {
	p := Dark.Theme()
	for _, tc := range []struct {
		name string
		c    uint8
		want float64
	}{
		{"scrollbar thumb", p.Scrollbar.A, 0.08},
		{"scrollbar hover", p.ScrollbarStyle.Hover.A, 0.16},
		{"scrollbar active", p.ScrollbarStyle.Active.A, 0.24},
		{"focus ring", p.Focus.A, 0.6},
	} {
		got := float64(tc.c) / 255
		if math.Abs(got-tc.want) > 0.02 {
			t.Errorf("%s alpha = %.3f, want %.2f (a byte written where a fraction belongs clamps to 1)",
				tc.name, got, tc.want)
		}
	}
}

// TestScrollbarTrackIsInvisible checks that the track draws nothing: a bar down
// every scrollable column is the one thing in a list that catches the eye more
// than the data in it.
func TestScrollbarTrackIsInvisible(t *testing.T) {
	if a := Dark.Theme().ScrollbarStyle.Track.A; a != 0 {
		t.Errorf("scrollbar track alpha = %d, want 0", a)
	}
}

// TestSidebarFillIsTransparentForMaterial checks the fill the source list paints.
//
// The window's material on macOS shows wherever the view draws no background, so
// a sidebar that fills an opaque colour hides it — and the translucency was the
// only reason to ask for the material at all.
func TestSidebarFillIsTransparentForMaterial(t *testing.T) {
	if got := sidebarFill().A; got != 0 {
		t.Errorf("sidebar fill alpha = %d, want 0 so the material shows", got)
	}
}
