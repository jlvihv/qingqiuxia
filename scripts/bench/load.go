// Command load is a load generator for measuring the core: it sends requests
// through the proxy at a chosen rate and reports throughput and latency.
//
// It is a development tool, not part of the core.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	var (
		proxyAddr = flag.String("proxy", "127.0.0.1:8899", "proxy address")
		target    = flag.String("target", "https://example.com/", "URL to request")
		conns     = flag.Int("c", 8, "concurrent clients")
		duration  = flag.Duration("d", 10*time.Second, "how long to send")
		insecure  = flag.Bool("k", true, "skip TLS verification of the origin")
		bodySize  = flag.Int("body", 0, "if positive, POST a body of this many bytes")
	)
	flag.Parse()

	pu, err := url.Parse("http://" + *proxyAddr)
	if err != nil {
		exit(err)
	}
	tr := &http.Transport{
		Proxy:               http.ProxyURL(pu),
		MaxIdleConnsPerHost: *conns,
		TLSClientConfig:     tlsConfig(*insecure),
	}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}

	var (
		done      = time.Now().Add(*duration)
		count     atomic.Int64
		errors    atomic.Int64
		bytes     atomic.Int64
		mu        sync.Mutex
		latencies []time.Duration
		wg        sync.WaitGroup
	)
	for i := 0; i < *conns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]time.Duration, 0, 4096)
			for time.Now().Before(done) {
				start := time.Now()
				var resp *http.Response
				var err error
				if *bodySize > 0 {
					resp, err = client.Post(*target, "application/octet-stream",
						io.LimitReader(neverEnding{}, int64(*bodySize)))
				} else {
					resp, err = client.Get(*target)
				}
				if err != nil {
					errors.Add(1)
					continue
				}
				n, _ := io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				bytes.Add(n)
				count.Add(1)
				local = append(local, time.Since(start))
			}
			mu.Lock()
			latencies = append(latencies, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	elapsed := duration.Seconds()
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p := func(q float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		i := int(q * float64(len(latencies)-1))
		return latencies[i]
	}
	fmt.Printf("requests %d  errors %d  rps %.0f  bytes %d\n",
		count.Load(), errors.Load(), float64(count.Load())/elapsed, bytes.Load())
	fmt.Printf("latency p50 %v  p90 %v  p99 %v  max %v\n",
		p(0.50).Round(time.Microsecond), p(0.90).Round(time.Microsecond),
		p(0.99).Round(time.Microsecond), p(1).Round(time.Microsecond))
}

// neverEnding yields zero bytes for ever, for a body of a known size.
type neverEnding struct{}

func (neverEnding) Read(b []byte) (int, error) {
	for i := range b {
		b[i] = 'x'
	}
	return len(b), nil
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
