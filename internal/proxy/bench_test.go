package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jlvihv/qingqiuxia/internal/proxy"
)

func BenchmarkPlainHTTP(b *testing.B) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer origin.Close()

	p := proxy.New(noopPolicy{}, proxy.Tuning{})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		b.Fatal(err)
	}
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

	p := proxy.New(noopPolicy{}, proxy.Tuning{MaxBodyBytes: 1 << 20})
	if _, err := p.Start("127.0.0.1:0"); err != nil {
		b.Fatal(err)
	}
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
