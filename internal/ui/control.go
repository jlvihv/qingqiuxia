package ui

// SetBackend gives the app the core it reads and the one it steers.
//
// They are separate interfaces because they are separate powers: reading
// traffic is what a remote client may do, while starting and stopping the proxy
// is what only the process that owns it may do. The command passes one object
// for both; the view cannot tell, and a remote client would have the first and
// not the second.
func (a *App) SetBackend(backend Backend, control Control) {
	a.backend = backend
	a.control = control
	if control != nil {
		if running, addr := control.ProxyStatus(); running {
			a.SetProxyRunning(addr)
		} else {
			a.SetProxyStopped()
		}
	}
}

// SetProxyRunning records that the proxy is listening, and where.
func (a *App) SetProxyRunning(addr string) {
	a.ProxyRunning = true
	a.PortAddr = addr
	a.ProxyAddr = addr
	a.PortError = ""
}

// SetProxyStopped records that nothing is listening: the port field goes back
// to being editable, holding the last address so that restarting on the same
// port is one click rather than retyping it, which is what a user who just
// stopped to change something else expects.
func (a *App) SetProxyStopped() {
	if a.PortText == "" && a.PortAddr != "" {
		a.PortText = a.PortAddr
	}
	a.ProxyRunning = false
	a.ProxyAddr = ""
	a.PortError = ""
}

// SetProxyError records why the proxy could not start, which the toolbar shows
// beside the field the user typed in.
func (a *App) SetProxyError(err error) {
	a.ProxyRunning = false
	a.PortError = err.Error()
}

// Bodies returns the chosen session's bodies as the panes currently hold them,
// and the id they belong to. It is what the panel drew, for a test that checks
// the text the panes were given rather than the widget that drew them.
func (a *App) Bodies() (Detail, uint64) {
	return a.bodies, a.detailID
}
