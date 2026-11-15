package proxy_test

import (
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

// proxyFor returns the proxy a stack runs, so a test can call it directly.
// The stack keeps the proxy in its own field, filled by startProxy.
func proxyFor(t *testing.T, s *stack) *proxy.Proxy {
	t.Helper()
	if s.proxy == nil {
		t.Fatal("the stack has no proxy")
	}
	return s.proxy
}
