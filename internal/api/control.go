package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
)

// The proxy's own lifecycle is part of the control plane, not of the proxy.
//
// Two interfaces exist and they are not the same thing:
//
//   - the proxy, a listener that forwards traffic for the client being
//     debugged: its address is what a browser is pointed at;
//   - the control API, a listener that answers questions and takes commands:
//     its address is what the window, a script or a browser of this tool talks
//     to.
//
// They are separate because they serve different callers with different
// lifetimes. A client keeps using the proxy across a capture toggle; a script
// keeps using the API while the proxy is stopped, which is exactly when it is
// most useful: starting it, choosing a port, reading why the last one failed.
// Hanging the control API on the proxy's lifetime would mean the way to start
// the proxy is unavailable while it is stopped.
//
// The pair is exposed here as routes on the control API, and as Go methods to
// the in-process window, which is one client of the same operations.

// ProxyController is what the control API needs to steer the proxy. It is
// separate from the traffic Policy on purpose: a caller may be allowed to watch
// traffic without being allowed to move the proxy.
type ProxyController interface {
	// ProxyStatus reports whether the proxy listens and where.
	ProxyStatus() (running bool, addr string)
	// StartProxy listens on addr and begins serving; it returns the address.
	StartProxy(addr string) (string, error)
	// StopProxy stops serving and releases the port.
	StopProxy() error
}

// routes for the proxy's lifecycle, registered with the rest of the API.
func (s *Server) proxyRoutes() {
	s.mux.HandleFunc("GET /api/proxy", s.handleProxyStatus)
	s.mux.HandleFunc("POST /api/proxy/start", s.handleProxyStart)
	s.mux.HandleFunc("POST /api/proxy/stop", s.handleProxyStop)
}

// proxyJSON is how the API shows the proxy.
type proxyJSON struct {
	// Running reports whether it listens, and Addr where.
	Running bool   `json:"running"`
	Addr    string `json:"addr,omitempty"`
}

func (s *Server) handleProxyStatus(w http.ResponseWriter, r *http.Request) {
	c := s.core.Proxy
	if c == nil {
		writeError(w, http.StatusNotImplemented, "this core has no proxy to steer")
		return
	}
	running, addr := c.ProxyStatus()
	writeJSON(w, http.StatusOK, proxyJSON{Running: running, Addr: addr})
}

// handleProxyStart listens on a port the caller chooses.
func (s *Server) handleProxyStart(w http.ResponseWriter, r *http.Request) {
	c := s.core.Proxy
	if c == nil {
		writeError(w, http.StatusNotImplemented, "this core has no proxy to steer")
		return
	}
	var body struct {
		// Addr is a port, an address, or empty for any free port.
		Addr string `json:"addr"`
	}
	if r.ContentLength > 0 {
		if !decode(w, r, &body) {
			return
		}
	}
	addr, err := c.StartProxy(body.Addr)
	if err != nil {
		// A port that is taken is the caller's mistake to see and fix, so the
		// reason goes back rather than a bare status.
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, proxyJSON{Running: true, Addr: addr})
}

func (s *Server) handleProxyStop(w http.ResponseWriter, r *http.Request) {
	c := s.core.Proxy
	if c == nil {
		writeError(w, http.StatusNotImplemented, "this core has no proxy to steer")
		return
	}
	if err := c.StopProxy(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, proxyJSON{Running: false})
}

var _ = context.Background
var _ = json.Marshal
var _ = strconv.Itoa
