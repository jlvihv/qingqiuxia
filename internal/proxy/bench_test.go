package proxy

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/msg"
)

// noopPolicy is the cheapest policy a proxy can have, so the benchmark
// measures the proxy's own overhead.
type noopPolicy struct{}

func (noopPolicy) Begin(ex *Exchange) {}
func (noopPolicy) Request(_ context.Context, _ *Exchange) (Decision, error) {
	return Decision{}, nil
}
func (noopPolicy) Response(_ context.Context, _ *Exchange, r *msg.Response) (*msg.Response, error) {
	return r, nil
}
func (noopPolicy) Complete(*Exchange, *msg.Response, error) {}
func (noopPolicy) Intercepts(string) bool                   { return false }
func (noopPolicy) Certificate(string) (*tls.Certificate, error) {
	return nil, nil
}

func BenchmarkPlainHTTP(b *testing.B) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer origin.Close()

	p := New(noopPolicy{}, Tuning{})
	if err := p.Listen("127.0.0.1:0"); err != nil {
		b.Fatal(err)
	}
	go p.Serve()
	defer p.Close()

	pu, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{
		Proxy:               http.ProxyURL(pu),
		MaxIdleConnsPerHost: 16,
	}}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := client.Get(origin.URL + "/api/test")
			if err != nil {
				b.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})
}

func BenchmarkCaptureLargeBody(b *testing.B) {
	payload := strings.Repeat("x", 64<<10)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(payload))
	}))
	defer origin.Close()

	p := New(noopPolicy{}, Tuning{MaxBodyBytes: 1 << 20})
	if err := p.Listen("127.0.0.1:0"); err != nil {
		b.Fatal(err)
	}
	go p.Serve()
	defer p.Close()

	pu, _ := url.Parse("http://" + p.Addr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, err := client.Get(origin.URL)
		if err != nil {
			b.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}
