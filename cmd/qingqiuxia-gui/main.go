// Command qingqiuxia-gui is the desktop window: a native interface over the
// core's control API.
//
// It is a separate binary from the core on purpose. The window is one client of
// the API, so the core keeps capturing with no window open, and the window can
// restart, or become a web page later, without touching the proxy.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// tuneRuntime bounds what the process spends before any window exists.
//
// Measured on a 32-core machine, idle, one window, with the GPU unavailable:
//
//	default                     183 MB RSS, 94 threads
//	GOMAXPROCS=4                168 MB RSS, 81 threads
//	GOMAXPROCS=4 LP_NUM_THREADS=2 184 MB RSS, 21 threads
//
// When the GPU works, its threads and its driver are worth their cost and none
// of this applies; when it does not, Mesa falls back to llvmpipe, which JITs
// shaders with libLLVM (163 MB resident) and spreads them over as many threads
// as there are cores. Both numbers are bounded here, whatever the renderer
// turns out to be:
//
//   - GOMAXPROCS: this window runs one goroutine of work on the main thread
//     plus a client goroutine. Thirty-two scheduler threads cost their stacks
//     and a slot each in every GC, for nothing.
//   - LP_NUM_THREADS: llvmpipe's rasteriser threads. A window redrawing a
//     table does not fill 32 of them; four keep scrolling smooth and stop the
//     process from holding a core's worth of warm threads while idle.
//
// A user who wants the defaults back sets the variables themselves: these only
// fill in what is unset.
func tuneRuntime() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(4)
	}
	if os.Getenv("LP_NUM_THREADS") == "" {
		os.Setenv("LP_NUM_THREADS", "4")
	}
}

func main() {
	tuneRuntime()

	var (
		apiAddr = flag.String("api", "127.0.0.1:8898", "the core's control API address")
		token   = flag.String("token", "", "the core's API token, if it has one")
		demo    = flag.Bool("demo", false, "show sample traffic instead of a live core")
	)
	flag.Parse()

	store := qqx.NewStore(5000)
	app := qqx.NewApp(store)

	var win *mygo.Window
	// invalidate asks for a frame after a goroutine changed the state. It runs
	// on the main thread, as the view does, so that the store is never read
	// while it is written.
	invalidate := func() {
		mygo.RunOnMain(func() {
			if win != nil {
				win.Invalidate()
			}
		})
	}

	if *demo {
		qqx.SeedDemo(store)
		app.ProxyAddr, app.CAInstalled, app.Version = "127.0.0.1:8899", true, "0.1.0-demo"
		app.CoreMemoryMB, app.CoreThreads = 118, 3
		app.Selected = 1
	} else {
		client := qqx.NewClient(*apiAddr, *token)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go client.Live(ctx, store, invalidate, func() {
			mygo.RunOnMain(func() {
				app.InvalidateAppend()
				if win != nil {
					win.Invalidate()
				}
			})
		})
		go followStatus(ctx, client, app, invalidate)
	}

	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "请求侠",
			Width:   1440,
			Height:  900,
			Content: ui.View(app.View),
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// followStatus keeps the toolbar's description of the core up to date, and
// reports whether it is running at all.
func followStatus(ctx context.Context, c *qqx.Client, app *qqx.App, invalidate func()) {
	// A status call every two seconds is cheap, and it tells the window when a
	// core starts or stops, which the event stream alone cannot.
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st, err := c.Status(ctx)
		if err != nil {
			app.SetCoreDown(err)
			invalidate()
			continue
		}
		app.SetCoreStatus(st)
		invalidate()
	}
}
