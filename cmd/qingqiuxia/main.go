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
	"encoding/json"
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
	pstore "github.com/jlvihv/qingqiuxia/internal/store"
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
	dataDir      string
	token        string
	maxBody      int64
	sessionLimit int
	interceptAll bool
	verbose      bool
	pprofAddr    string
	headless     bool
	// keepSessions and keepDays bound what the database keeps.
	keepSessions int
	keepDays     int
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.proxyAddr, "proxy", "", "address the proxy listens on; empty starts with the proxy stopped, and the window offers a port")
	flag.StringVar(&cfg.apiAddr, "api", "127.0.0.1:8898", "address the control API listens on, when -headless")
	flag.StringVar(&cfg.dataDir, "data", defaultDataDir(), "directory for the certificate authority and saved rules")
	flag.StringVar(&cfg.token, "token", "", "API token; empty means no token, which is only safe on loopback or a socket")
	flag.Int64Var(&cfg.maxBody, "max-body", proxy.DefaultMaxBodyBytes, "how much of a body to keep for inspection, in bytes")
	flag.IntVar(&cfg.sessionLimit, "sessions", 2000, "how many sessions to keep in memory")
	flag.BoolVar(&cfg.interceptAll, "intercept", true, "decrypt TLS traffic")
	flag.BoolVar(&cfg.verbose, "v", false, "log every exchange")
	flag.StringVar(&cfg.pprofAddr, "pprof", "", "if set, serve pprof and expvar on this address")
	flag.BoolVar(&cfg.headless, "headless", false, "run the core without a window")
	flag.IntVar(&cfg.keepSessions, "keep-sessions", 2000, "数据库里最多留多少条历史流量")
	flag.IntVar(&cfg.keepDays, "keep-days", 7, "历史流量最多留几天")
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
	rules    *rule.Engine
	store    *pstore.DB
	cfg      config
}

// buildCore assembles the proxy, the rules, the session store and the API.
func buildCore(cfg config) (*core, error) {
	ca, err := mitm.LoadOrCreate(cfg.dataDir, "Qingqiuxia Root CA")
	if err != nil {
		return nil, err
	}
	db, err := pstore.Open(context.Background(), filepath.Join(cfg.dataDir, "qingqiuxia.db"))
	if err != nil {
		return nil, err
	}

	rules := rule.NewEngine()
	// 规则从数据库读：那是人写的东西，丢了要重写。
	if err := loadRules(context.Background(), db, rules); err != nil {
		db.Close()
		return nil, err
	}
	sessions := session.NewStore(cfg.sessionLimit)
	issuer := mitm.NewIssuer(ca)
	pol := pipeline.New(pipeline.Options{
		Rules:       rules,
		Breakpoints: breakpoint.NewManager(0),
		Sessions:    sessions,
		Issuer:      issuer,
		ShouldIntercept: func(string) bool {
			return cfg.interceptAll
		},
	})

	pr := proxy.New(pol, proxy.Tuning{MaxBodyBytes: cfg.maxBody})
	if cfg.verbose {
		pr.SetLogger(log.Default())
	}
	// The proxy starts only when an address was asked for. A window that opens
	// with nothing listening is the normal case: the user picks the port, and
	// picks it again when it is taken, rather than the program failing to start
	// because a number it chose for them was busy.
	if cfg.proxyAddr != "" {
		if _, err := pr.Start(cfg.proxyAddr); err != nil {
			db.Close()
			return nil, fmt.Errorf("start the proxy on %s: %w", cfg.proxyAddr, err)
		}
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
		// A leaf certificate per host is the cost of HTTPS capture, and the
		// status call is where a user sees it.
		CachedCertificates: issuer.CachedCount,
		RulesChanged: func(specs []rule.Spec) {
			// 规则一改就落盘：写规则的人不想因为窗口崩了就重写一遍。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := db.SaveRules(ctx, specs); err != nil {
				log.Printf("保存规则失败: %v", err)
			}
		},
		Proxy: &proxyControl{
			start:  pr.Start,
			stop:   pr.Stop,
			status: func() (bool, string) { return pr.Running(), pr.Addr() },
		},
		Logger: log.Default(),
	}, api.Options{Token: cfg.token})

	// The control API always runs. It is not a fallback for having no window:
	// it is the core's management face, and it is the only way to reach the
	// core from another process — a script, a browser, or the window of a
	// second machine. The window does not need it (it calls the core
	// directly), and that is exactly why its presence costs nothing.
	if err := server.Listen(cfg.apiAddr); err != nil {
		pr.Close()
		return nil, fmt.Errorf("control API on %s: %w", cfg.apiAddr, err)
	}
	return &core{
		proxy: pr, server: server, pipeline: pol,
		rules: rules, store: db, cfg: cfg,
	}, nil
}

// log says where the proxy is and what to install.
func (c *core) log() {
	log.Printf("qingqiuxia %s", version)
	// The two interfaces, named so that they cannot be mistaken for each other.
	if addr := c.proxy.Addr(); addr != "" {
		log.Printf("proxy   http://%s  (point your client's proxy here)", addr)
	} else {
		log.Printf("proxy   stopped; start it from the window or POST /api/proxy/start")
	}
	log.Printf("control http://%s/api/status  (the tool's API, not the proxy)", c.server.Addr())
	log.Printf("ca      %s", filepath.Join(c.cfg.dataDir, "ca.crt"))
	log.Printf("install the CA certificate in your client to see HTTPS traffic")
}

// serve runs the core alone until a signal stops it.
func (c *core) serve(pprofAddr string) error {
	errs := make(chan error, 2)
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
//
// Nothing here goes over a network. The window holds the core's own types and
// calls it, so a session reaches the table as the value the proxy recorded,
// not as JSON that was encoded and decoded to cross a socket.
func (c *core) serveWithWindow() error {
	if c.cfg.pprofAddr != "" {
		go func() {
			log.Printf("pprof   http://%s/debug/pprof/", c.cfg.pprofAddr)
			serveProfiling(c.cfg.pprofAddr)
		}()
	}

	// The control API is a listener like the proxy is, and a listener that is
	// not served accepts connections and answers none: the window does not need
	// it, so forgetting this went unnoticed until a script tried.
	go func() {
		if err := c.server.Serve(); err != nil {
			log.Printf("control API stopped: %v", err)
		}
	}()

	local := &qqx.Local{
		Pipeline: c.pipeline,
		Proxy:    c.proxy,
		Version:  version,
		Started:  time.Now(),
	}

	store := qqx.NewStore(c.cfg.sessionLimit)
	app := qqx.NewApp(store)
	// Nothing is chosen at start-up: the window opens on the traffic list, and
	// the inspector appears when the user picks a row.
	app.SetBackend(local, local)

	// 上次的窗口形状和近期流量，在开窗之前恢复：窗口一出来就该是上次的样子，
	// 而不是先闪一下默认布局再跳。
	saved := c.restoreSession(store, app)

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
			if err := local.SetCapture(ctx, on); err == nil {
				app.SetCapturing(on)
				invalidate()
			}
		}()
	}
	app.OnResume = func(id uint64) {
		go func() {
			if err := local.Resume(ctx, id, false); err == nil {
				app.ClearPause()
				invalidate()
			}
		}()
	}
	app.OnAbort = func(id uint64) {
		go func() {
			if err := local.Resume(ctx, id, true); err == nil {
				app.ClearPause()
				invalidate()
			}
		}()
	}

	// The proxy's switch: the window offers the port, the core opens it, and a
	// port that is taken is an error the window shows rather than a start that
	// fails.
	app.OnStartProxy = func(addr string) {
		go func() {
			got, err := local.StartProxy(addr)
			mygo.RunOnMain(func() {
				if err != nil {
					app.SetProxyError(err)
				} else {
					app.SetProxyRunning(got)
				}
				if win != nil {
					win.Invalidate()
				}
			})
		}()
	}
	app.OnStopProxy = func() {
		go func() {
			err := local.StopProxy()
			mygo.RunOnMain(func() {
				if err != nil {
					app.SetProxyError(err)
				} else {
					app.SetProxyStopped()
				}
				if win != nil {
					win.Invalidate()
				}
			})
		}()
	}
	// A proxy that was started from the command line shows as running.
	if running, addr := local.ProxyStatus(); running {
		app.SetProxyRunning(addr)
	} else {
		app.SetProxyStopped()
	}

	go follow(ctx, local, store, app, invalidate)

	mygo.App.WhenReady(func() {
		w, h := float32(1440), float32(900)
		if saved.Width > 400 && saved.Height > 300 {
			w, h = saved.Width, saved.Height
		}
		opts := mygo.WindowOptions{
			Title:     "请求侠",
			Width:     int(w),
			Height:    int(h),
			MinWidth:  900,
			MinHeight: 560,
			// The window remembers its own place, size and maximized state, which
			// is what the saved shape was for. It also remembers which display it
			// was on, which a width and a height cannot.
			StateKey: "main",
			Content:  ui.View(app.View),
		}
		if runtime.GOOS == "darwin" {
			// macOS: no title bar of its own, so the traffic lights sit inside
			// the toolbar and the sidebar gets the window's material, the way
			// every tool window on the platform works.
			opts.TitleBarStyle = mygo.TitleBarHiddenInset
			opts.TrafficLightPosition = &mygo.Point{X: 20, Y: 20}
			opts.Vibrancy = mygo.VibrancySidebar
		}
		win = mygo.NewWindow(opts)
	})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		mygo.RunOnMain(func() { mygo.App.Quit() })
	}()
	err := mygo.App.Run()
	// The window is gone and its shape is final: write it down here, which is the
	// only place that knows both the widgets and the moment they stopped. It was
	// never called before, so the window opened at its default size every time
	// even though the store had a row for it.
	if win != nil {
		if w, h := win.Size(); w > 0 && h > 0 {
			app.WindowWidth, app.WindowHeight = float32(w), float32(h)
		}
	}
	c.rememberWindow(app)
	return err
}

// follow keeps the window's copy of the core's state current: the traffic
// through Watch, and the toolbar's description on a slow tick, which is enough
// for numbers that change when a session does.
func follow(ctx context.Context, local *qqx.Local, store *qqx.Store, app *qqx.App, invalidate func()) {
	go func() {
		_ = local.Watch(ctx, func(ch qqx.Change) {
			switch ch.Kind {
			case qqx.ChangeSession:
				if ch.Session != nil {
					store.Upsert(*ch.Session)
				}
				mygo.RunOnMain(func() {
					if ch.Added && ch.Session != nil {
						app.InvalidateAppend(ch.Session.ID)
					} else {
						// An update rewrites a row where it is; nothing moves.
						app.Invalidate()
					}
					invalidate()
				})
			case qqx.ChangeDropped:
				if ch.Session != nil {
					store.Remove(ch.Session.ID)
				}
				mygo.RunOnMain(func() {
					app.Invalidate()
					invalidate()
				})
			case qqx.ChangePaused:
				mygo.RunOnMain(func() {
					app.SetPause(ch.Pause)
					invalidate()
				})
			case qqx.ChangeResumed:
				mygo.RunOnMain(func() {
					app.ClearPause()
					invalidate()
				})
			}
		})
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st, err := local.Status(ctx)
		mygo.RunOnMain(func() {
			if err != nil {
				app.SetCoreDown(err)
			} else {
				app.SetCoreStatus(st)
			}
			if running, addr := local.ProxyStatus(); running {
				app.SetProxyRunning(addr)
			}
			invalidate()
		})
	}
}

// close stops everything, in the order that leaves no client hanging.
func (c *core) close() {
	c.server.Close()
	c.proxy.Close()
	if c.store != nil {
		if err := c.saveState(); err != nil {
			log.Printf("保存状态失败: %v", err)
		}
		c.store.Close()
	}
}

// saveState writes what should outlive this run: the rules, and the traffic
// still worth reading.
func (c *core) saveState() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if specs := rule.Specs(c.rules.Rules()); len(specs) > 0 || c.store != nil {
		if err := c.store.SaveRules(ctx, specs); err != nil {
			return fmt.Errorf("保存规则: %w", err)
		}
	}

	// The traffic: the sessions the core still holds, which is what a user
	// would have seen on screen.
	list := c.pipeline.Sessions().List(0, -1)
	records := make([]pstore.Record, 0, len(list))
	for _, snap := range list {
		records = append(records, recordOfSnapshot(snap))
	}
	if err := c.store.SaveSessions(ctx, records); err != nil {
		return fmt.Errorf("保存流量: %w", err)
	}
	if err := c.store.Prune(ctx, c.cfg.keepSessions, time.Duration(c.cfg.keepDays)*24*time.Hour); err != nil {
		return fmt.Errorf("清理旧流量: %w", err)
	}
	return nil
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
func loadRules(ctx context.Context, db *pstore.DB, e *rule.Engine) error {
	specs, err := db.LoadRules(ctx)
	if err != nil {
		return fmt.Errorf("读规则: %w", err)
	}
	if len(specs) == 0 {
		// 早期版本把规则存在 rules.json 里，第一次启动新版本时搬过来。
		specs = migrateRulesFile(filepath.Join(defaultDataDir(), "rules.json"))
	}
	if len(specs) == 0 {
		return nil
	}
	if err := e.Replace(specs); err != nil {
		return fmt.Errorf("恢复规则: %w", err)
	}
	log.Printf("恢复了 %d 条规则", len(specs))
	return nil
}

// migrateRulesFile reads the old rules.json, once.
func migrateRulesFile(path string) []rule.Spec {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	specs, err := rule.ParseSpecs(data)
	if err != nil {
		return nil
	}
	log.Printf("从 %s 搬来 %d 条规则", path, len(specs))
	return specs
}

// recordOfSnapshot turns a session into what the store keeps.
func recordOfSnapshot(s session.Snapshot) pstore.Record {
	r := pstore.Record{
		ID:       s.ID,
		Started:  s.Timings.Start,
		Phase:    s.Phase.String(),
		Source:   s.Source.String(),
		Duration: s.Timings.Duration().Seconds() * 1000,
		Bytes:    s.BytesOut,
	}
	if s.Request != nil {
		r.Method = s.Request.Method
		if s.Request.URL != nil {
			r.URL = s.Request.URL.String()
			r.Host = s.Request.URL.Host
		}
	}
	if s.Response != nil {
		r.Status = s.Response.Status
	}
	if len(s.Notes) > 0 {
		if data, err := json.Marshal(s.Notes); err == nil {
			r.Notes = string(data)
		}
	}
	return r
}

// proxyControl adapts the proxy to the control API's ProxyController, which is
// what lets a script start and stop the proxy without the API importing the
// proxy package.
type proxyControl struct {
	start  func(addr string) (string, error)
	stop   func() error
	status func() (bool, string)
}

func (p *proxyControl) ProxyStatus() (bool, string) { return p.status() }
func (p *proxyControl) StartProxy(addr string) (string, error) {
	return p.start(addr)
}
func (p *proxyControl) StopProxy() error { return p.stop() }

// restoreSession brings back what the last run left: the window's shape, and the
// traffic it was showing.
//
// 流量只恢复最近的几百条，不恢复全部：那是接着上次继续看用的痕迹，不是归档。
// 太多会让启动变慢，也让用户以为这次也抓到了这么多。
func (c *core) restoreSession(store *qqx.Store, app *qqx.App) pstore.Window {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	w, ok, err := c.store.LoadWindow(ctx)
	if err != nil {
		log.Printf("读窗口状态失败: %v", err)
	} else if ok {
		if w.SidebarWidth > 60 {
			app.SidebarWidth = w.SidebarWidth
		}
		if w.MainWidth > 120 {
			app.MainWidth = w.MainWidth
		}
		app.Detail = w.Detail
		app.Search = w.Search
		app.Filter.Text = w.Search
	}

	records, err := c.store.LoadSessions(ctx, c.cfg.sessionLimit)
	if err != nil {
		log.Printf("读历史流量失败: %v", err)
		return w
	}
	// 存储里是最新在前，而 store 是往前插，所以反过来喂。
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		store.Upsert(qqx.Session{
			ID: r.ID, Phase: r.Phase, Source: r.Source, Method: r.Method,
			URL: r.URL, Host: r.Host, Status: r.Status,
			DurationMS: r.Duration, BytesOut: r.Bytes, Started: r.Started,
			Notes: notesOf(r.Notes),
		})
	}
	if n := store.Len(); n > 0 {
		log.Printf("恢复了 %d 条历史流量", n)
	}
	app.Invalidate()
	return w
}

// rememberWindow stores the window's shape for the next run.
func (c *core) rememberWindow(app *qqx.App) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	w := pstore.Window{
		Width:        app.WindowWidth,
		Height:       app.WindowHeight,
		SidebarWidth: app.SidebarWidth,
		MainWidth:    app.MainWidth,
		Detail:       app.Detail,
		Search:       app.Search,
	}
	if err := c.store.SaveWindow(ctx, w); err != nil {
		log.Printf("保存窗口状态失败: %v", err)
	}
}

// notesOf reads the notes a record kept.
func notesOf(text string) []qqx.Note {
	if text == "" {
		return nil
	}
	var notes []qqx.Note
	if err := json.Unmarshal([]byte(text), &notes); err != nil {
		return nil
	}
	return notes
}
