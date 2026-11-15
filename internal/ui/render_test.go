package ui_test

import (
	"image/png"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// TestRenderLayout draws the window once, without a display, and writes it to a
// file: the way to look at the layout on a machine with no desktop, and the
// start of a snapshot test.
func TestRenderLayout(t *testing.T) {
	out := os.Getenv("QQX_RENDER")
	if out == "" {
		t.Skip("set QQX_RENDER=/path.png to render the window")
	}
	store := qqx.NewStore(100)
	qqx.SeedDemo(store)
	app := qqx.NewApp(store)
	app.ProxyAddr = "127.0.0.1:8899"
	app.CAInstalled = true
	app.Version = "0.1.0"
	app.CoreMemoryMB = 118
	app.CoreThreads = 3
	app.Selected = 1

	img := ui.Render(app.View, 1440, 900, 1)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
