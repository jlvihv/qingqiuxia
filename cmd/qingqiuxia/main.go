// Command qingqiuxia is both the core and its window.
//
// The core — the proxy, the rules, the breakpoints, the session store and the
// control API — runs in this process, and so does the window. They are not
// entangled: the window reaches the core through the same HTTP API any other
// client would, over a unix socket that only this process can open. That keeps
// the boundary the earlier split had (the view knows nothing of the proxy) and
// drops what the split cost (a second process, a port to pick, a token to
// keep, and a window that could outlive the core it was showing).
//
// -headless runs the core alone, for a machine with no display or a CI job that
// only needs the proxy and its API.
package main

import (
	"context"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// version is the build's version.
var version = "0.1.0-dev"

func main() {
	tuneRuntime()

	cfg := parseFlags()
	if err := cfg.validate(); err != nil {
		log.Fatalf("qingqiuxia: %v", err)
	}
	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		log.Fatalf("qingqiuxia: %v", err)
	}

	core, err := buildCore(cfg)
	if err != nil {
		log.Fatalf("qingqiuxia: %v", err)
	}
	defer core.close()
	core.log()

	if cfg.headless {
		if err := core.serve(cfg.pprofAddr); err != nil {
			log.Fatalf("qingqiuxia: %v", err)
		}
		return
	}
	if err := core.serveWithWindow(); err != nil {
		log.Fatalf("qingqiuxia: %v", err)
	}
}

// tuneRuntime bounds what the process spends on threads, before any window
// exists.
//
// Measured on a 32-core machine, idle, one window. GOMAXPROCS=4 alone took 94
// threads to 81; the rest were llvmpipe's rasteriser, one per core, which
// LP_NUM_THREADS bounds:
//
//	default            183 MB, 94 threads
//	both bounded        21 threads
//
// A window drawing a table needs neither 32 scheduler threads nor 32
// rasteriser threads. With a working GPU the second does not apply, and setting
// it costs nothing: only llvmpipe reads it. A user who wants the defaults back
// sets the variables themselves.
func tuneRuntime() {
	if os.Getenv("GOMAXPROCS") == "" {
		runtime.GOMAXPROCS(4)
	}
	if os.Getenv("LP_NUM_THREADS") == "" {
		os.Setenv("LP_NUM_THREADS", "4")
	}
}

// config is what the command line says.
type config struct {
	proxyAddr    string
	apiAddr      string
	socketPath   string
	dataDir      string
	token        string
	maxBody      int64
	sessionLimit int
	interceptAll bool
	verbose      bool
	pprofAddr    string
	headless     bool
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.proxyAddr, "proxy", "127.0.0.1:8899", "address the proxy listens on")
	flag.StringVar(&cfg.apiAddr, "api", "127.0.0.1:8898", "address the control API listens on, when -headless")
	flag.StringVar(&cfg.socketPath, "socket", "", "unix socket for the control API; empty uses a private one beside the certificate")
	flag.StringVar(&cfg.dataDir, "data", defaultDataDir(), "directory for the certificate authority and saved rules")
	flag.StringVar(&cfg.token, "token", "", "API token; empty means no token, which is only safe on loopback or a socket")
	flag.Int64Var(&cfg.maxBody, "max-body", proxy.DefaultMaxBodyBytes, "how much of a body to keep for inspection, in bytes")
	flag.IntVar(&cfg.sessionLimit, "sessions", 2000, "how many sessions to keep in memory")
	flag.BoolVar(&cfg.interceptAll, "intercept", true, "decrypt TLS traffic")
	flag.BoolVar(&cfg.verbose, "v", false, "log every exchange")
	flag.StringVar(&cfg.pprofAddr, "pprof", "", "if set, serve pprof and expvar on this address")
	flag.BoolVar(&cfg.headless, "headless", false, "run the core without a window")
	flag.Parse()
	return cfg
}

func (c config) validate() error {
	if c.headless && c.token == "" && !isLoopback(c.apiAddr) {
		return fmt.Errorf("the API would listen on %s with no token: pass -token, or bind it to loopback", c.apiAddr)
	}
	if c.maxBody <= 0 {
		return fmt.Errorf("-max-body must be positive")
	}
	if c.sessionLimit <= 0 {
		return fmt.Errorf("-sessions must be positive")
	}
	return nil
}

// isLoopback reports whether addr names the local host only.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// core is the assembled process: the proxy, the API and what they share.
type core struct {
	proxy    *proxy.Proxy
	server   *api.Server
	pipeline *pipeline.Pipeline
	cfg      config
	// apiAddr is what the window connects to: a socket path, or the TCP
	// address in headless mode.
	apiAddr string
	// socketPath is the socket to remove on the way out, empty when the API is
	// on TCP.
	socketPath string
}

// buildCore assembles the proxy, the rules, the session store and the API.
func buildCore(cfg config) (*core, error) {
	ca, err := mitm.LoadOrCreate(cfg.dataDir, "Qingqiuxia Root CA")
	if err != nil {
		return nil, err
	}
	rules := rule.NewEngine()
	if err := loadRules(cfg.dataDir, rules); err != nil {
		return nil, err
	}
	sessions := session.NewStore(cfg.sessionLimit)
	pol := pipeline.New(pipeline.Options{
		Rules:       rules,
		Breakpoints: breakpoint.NewManager(0),
		Sessions:    sessions,
		Issuer:      mitm.NewIssuer(ca),
		ShouldIntercept: func(string) bool {
			return cfg.interceptAll
		},
	})

	pr := proxy.New(pol, proxy.Tuning{MaxBodyBytes: cfg.maxBody})
	if cfg.verbose {
		pr.SetLogger(log.Default())
	}
	if err := pr.Listen(cfg.proxyAddr); err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.proxyAddr, err)
	}

	server := api.New(api.Core{
		Pipeline:   pol,
		Version:    version,
		Started:    time.Now(),
		Addr:       pr.Addr,
		CaptureOn:  pol.Recording,
		SetCapture: pol.SetRecording,
		Repeat:     repeater(pr),
		Resources:  api.ProcessResources,
		Logger:     log.Default(),
	}, api.Options{Token: cfg.token})

	// Without a window, the API is reachable on TCP so a script or a browser
	// can drive it. With one, it is a socket: the window is the only client,
	// and a socket needs no port, no token and no firewall consideration.
	apiAddr, socketPath := cfg.socketPath, ""
	if cfg.headless {
		apiAddr = cfg.apiAddr
	} else {
		if apiAddr == "" {
			apiAddr = filepath.Join(cfg.dataDir, "core.sock")
		}
		socketPath = apiAddr
	}
	if err := server.Listen(apiAddr); err != nil {
		pr.Close()
		return nil, fmt.Errorf("listen on %s: %w", apiAddr, err)
	}
	return &core{
		proxy: pr, server: server, pipeline: pol, cfg: cfg,
		apiAddr: apiAddr, socketPath: socketPath,
	}, nil
}

// log says where the proxy is and what to install.
func (c *core) log() {
	log.Printf("qingqiuxia %s", version)
	log.Printf("proxy   http://%s  (set this as your HTTP and HTTPS proxy)", c.proxy.Addr())
	if c.socketPath != "" {
		log.Printf("api     unix://%s", c.socketPath)
	} else {
		log.Printf("api     http://%s/api/status", c.server.Addr())
	}
	log.Printf("ca      %s", filepath.Join(c.cfg.dataDir, "ca.crt"))
	log.Printf("install the CA certificate in your client to see HTTPS traffic")
}

// serve runs the core alone until a signal stops it.
func (c *core) serve(pprofAddr string) error {
	errs := make(chan error, 2)
	go func() { errs <- c.proxy.Serve() }()
	go func() { errs <- c.server.Serve() }()
	if pprofAddr != "" {
		go func() { errs <- serveProfiling(pprofAddr) }()
		log.Printf("pprof   http://%s/debug/pprof/", pprofAddr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
		log.Printf("shutting down")
		return nil
	case err := <-errs:
		return err
	}
}

// serveWithWindow runs the core and shows it: the core in goroutines, the
// window on the main thread, which is where MyGo must run.
func (c *core) serveWithWindow() error {
	if c.cfg.pprofAddr != "" {
		go func() {
			log.Printf("pprof   http://%s/debug/pprof/", c.cfg.pprofAddr)
			serveProfiling(c.cfg.pprofAddr)
		}()
	}
	go func() {
		if err := c.proxy.Serve(); err != nil {
			log.Printf("proxy stopped: %v", err)
		}
	}()
	go func() {
		if err := c.server.Serve(); err != nil {
			log.Printf("api stopped: %v", err)
		}
	}()

	store := qqx.NewStore(c.cfg.sessionLimit)
	app := qqx.NewApp(store)
	// The window starts with the newest row chosen, so the detail panel has
	// something to show instead of an empty half of the window.
	app.Selected = 0

	client := qqx.NewUnixClient(c.apiAddr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var win *mygo.Window
	invalidate := func() {
		mygo.RunOnMain(func() {
			if win != nil {
				win.Invalidate()
			}
		})
	}
	app.OnToggleCapture = func(on bool) {
		go func() {
			if err := client.SetCapture(ctx, on); err == nil {
				app.SetCapturing(on)
				invalidate()
			}
		}()
	}
	app.OnResume = func(id uint64) {
		go func() {
			if err := client.Resume(ctx, id); err == nil {
				app.ClearPause()
				invalidate()
			}
		}()
	}
	app.OnAbort = func(id uint64) {
		go func() {
			if err := client.AbortPause(ctx, id); err == nil {
				app.ClearPause()
				invalidate()
			}
		}()
	}

	go client.Live(ctx, store, invalidate, func() {
		mygo.RunOnMain(func() {
			app.InvalidateAppend()
			if win != nil {
				win.Invalidate()
			}
		})
	})
	go followStatus(ctx, client, app, invalidate)
	go followPauses(ctx, client, app, invalidate)

	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:   "请求侠",
			Width:   1440,
			Height:  900,
			Content: ui.View(app.View),
		})
	})
	// A signal closes the window, which is also how the core stops: the
	// process owns both, so leaving the window ends them together.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		mygo.RunOnMain(func() { mygo.App.Quit() })
	}()
	return mygo.App.Run()
}

// close stops everything, in the order that leaves no client hanging.
func (c *core) close() {
	c.server.Close()
	c.proxy.Close()
	if c.socketPath != "" {
		os.Remove(c.socketPath)
	}
}

// followStatus keeps the toolbar's description of the core up to date, and
// reports whether it is running at all.
func followStatus(ctx context.Context, cl *qqx.Client, app *qqx.App, invalidate func()) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st, err := cl.Status(ctx)
		if err != nil {
			app.SetCoreDown(err)
			invalidate()
			continue
		}
		app.SetCoreStatus(st)
		invalidate()
	}
}

// followPauses keeps the breakpoint banner in step with the core.
func followPauses(ctx context.Context, cl *qqx.Client, app *qqx.App, invalidate func()) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	last := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		pauses, err := cl.Pauses(ctx)
		if err != nil {
			continue
		}
		if len(pauses) == 0 {
			if last != 0 {
				app.ClearPause()
				last = 0
				invalidate()
			}
			continue
		}
		p := pauses[0]
		if p.ID != last {
			app.SetPause(&p)
			last = p.ID
			invalidate()
		}
	}
}

// repeater re-sends a recorded request, for the API's replay call.
func repeater(pr *proxy.Proxy) func(s session.Snapshot, count int, delay, interval time.Duration) {
	return func(s session.Snapshot, count int, delay, interval time.Duration) {
		if s.Request == nil || s.Request.URL == nil {
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		for i := 0; i < count; i++ {
			if i > 0 && interval > 0 {
				time.Sleep(interval)
			}
			if err := pr.Replay(context.Background(), s.Request); err != nil {
				log.Printf("repeat %d: %v", s.ID, err)
			}
		}
	}
}

// serveProfiling starts the standard library's profiling endpoints.
func serveProfiling(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/vars", expvar.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// defaultDataDir is where the certificate and saved rules live.
func defaultDataDir() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "qingqiuxia")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".qingqiuxia"
	}
	return filepath.Join(home, ".config", "qingqiuxia")
}

// loadRules reads the saved rules, if there are any.
func loadRules(dir string, e *rule.Engine) error {
	path := filepath.Join(dir, "rules.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	specs, err := rule.ParseSpecs(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := e.Replace(specs); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	log.Printf("loaded %d rules from %s", len(specs), path)
	return nil
}
