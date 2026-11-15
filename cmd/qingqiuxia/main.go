// Command qingqiuxia runs the core: the MITM proxy, the rule engine, the
// breakpoints, the session store and the control API, in one process.
//
// It has no window. A GUI, a script or a browser drives it through the API, so
// the same binary serves a desktop app, a CI job and a remote debugging session.
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
	"sync"
	"syscall"
	"time"

	"github.com/jlvihv/qingqiuxia/internal/api"
	"github.com/jlvihv/qingqiuxia/internal/breakpoint"
	"github.com/jlvihv/qingqiuxia/internal/mitm"
	"github.com/jlvihv/qingqiuxia/internal/pipeline"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
	"github.com/jlvihv/qingqiuxia/internal/rule"
	"github.com/jlvihv/qingqiuxia/internal/session"
)

// version is the core's version, set by the build.
var version = "0.1.0-dev"

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	if err := run(); err != nil {
		log.Fatalf("qingqiuxia: %v", err)
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
	pprof        string
}

func run() error {
	var cfg config
	flag.StringVar(&cfg.proxyAddr, "proxy", "127.0.0.1:8899", "address the proxy listens on")
	flag.StringVar(&cfg.apiAddr, "api", "127.0.0.1:8898", "address the control API listens on")
	flag.StringVar(&cfg.dataDir, "data", defaultDataDir(), "directory for the certificate authority and saved rules")
	flag.StringVar(&cfg.token, "token", "", "API token; empty means no token, which is only safe on loopback")
	flag.Int64Var(&cfg.maxBody, "max-body", proxy.DefaultMaxBodyBytes, "how much of a body to keep for inspection, in bytes")
	flag.IntVar(&cfg.sessionLimit, "sessions", 2000, "how many sessions to keep in memory")
	flag.BoolVar(&cfg.interceptAll, "intercept", true, "decrypt TLS traffic")
	flag.BoolVar(&cfg.verbose, "v", false, "log every exchange")
	flag.StringVar(&cfg.pprof, "pprof", "", "if set, serve pprof and expvar on this address, for profiling the core")
	flag.Parse()

	if err := os.MkdirAll(cfg.dataDir, 0o700); err != nil {
		return err
	}

	ca, err := mitm.LoadOrCreate(cfg.dataDir, "Qingqiuxia Root CA")
	if err != nil {
		return err
	}
	issuer := mitm.NewIssuer(ca)

	rules := rule.NewEngine()
	if err := loadRules(cfg.dataDir, rules); err != nil {
		return err
	}
	pauses := breakpoint.NewManager(0)
	sessions := session.NewStore(cfg.sessionLimit)

	pol := pipeline.New(pipeline.Options{
		Rules:       rules,
		Breakpoints: pauses,
		Sessions:    sessions,
		Issuer:      issuer,
		ShouldIntercept: func(host string) bool {
			return cfg.interceptAll
		},
	})

	p := proxy.New(pol, proxy.Tuning{MaxBodyBytes: cfg.maxBody})
	if cfg.verbose {
		p.SetLogger(log.Default())
	}
	if err := p.Listen(cfg.proxyAddr); err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.proxyAddr, err)
	}

	core := &api.Core{
		Pipeline:  pol,
		Version:   version,
		Started:   time.Now(),
		Addr:      p.Addr,
		CaptureOn: pol.Recording,
		SetCapture: func(on bool) {
			// Capture is the proxy's presence: turning it off stops recording
			// new sessions while the proxy keeps forwarding.
			pol.SetRecording(on)
		},
		Repeat: repeater(p),
		Logger: log.Default(),
	}
	server := api.New(*core, api.Options{Token: cfg.token})
	if err := server.Listen(cfg.apiAddr); err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.apiAddr, err)
	}

	log.Printf("qingqiuxia %s", version)
	log.Printf("proxy   http://%s  (set this as your HTTP and HTTPS proxy)", p.Addr())
	log.Printf("api     http://%s/api/status", server.Addr())
	log.Printf("ca      %s", filepath.Join(cfg.dataDir, "ca.crt"))
	log.Printf("install the CA certificate in your client to see HTTPS traffic")

	errs := make(chan error, 3)
	go func() { errs <- p.Serve() }()
	go func() { errs <- server.Serve() }()
	if cfg.pprof != "" {
		go func() { errs <- serveProfiling(cfg.pprof) }()
		log.Printf("pprof   http://%s/debug/pprof/", cfg.pprof)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case <-ctx.Done():
		log.Printf("shutting down")
	case err := <-errs:
		if err != nil {
			p.Close()
			server.Close()
			return err
		}
	}
	server.Close()
	return p.Close()
}

// serveProfiling starts the standard library's profiling endpoints, which are
// the way to find where the core spends its time under load.
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

// repeater returns a function that re-sends a recorded request, for the API's
// replay call.
func repeater(p *proxy.Proxy) func(s session.Snapshot, count int, delay, interval time.Duration) {
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
			if err := p.Replay(context.Background(), s.Request); err != nil {
				log.Printf("repeat %d: %v", s.ID, err)
			}
		}
	}
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

var _ = net.JoinHostPort
var _ = sync.Once{}
