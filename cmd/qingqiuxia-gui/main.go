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
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

func main() {
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
		go client.Live(ctx, store, invalidate)
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
