package proxy_test

import (
	"context"
	"crypto/tls"

	"github.com/jlvihv/qingqiuxia/internal/msg"
	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

// noopPolicy is the cheapest policy a proxy can have: it records nothing and
// changes nothing, so a test measures the proxy alone.
type noopPolicy struct{}

func (noopPolicy) Begin(*proxy.Exchange) {}
func (noopPolicy) Request(_ context.Context, _ *proxy.Exchange) (proxy.Decision, error) {
	return proxy.Decision{}, nil
}
func (noopPolicy) Response(_ context.Context, _ *proxy.Exchange, r *msg.Response) (*msg.Response, error) {
	return r, nil
}
func (noopPolicy) Complete(*proxy.Exchange, *msg.Response, error) {}
func (noopPolicy) Intercepts(string) bool                         { return false }
func (noopPolicy) Certificate(string) (*tls.Certificate, error) {
	return nil, nil
}
