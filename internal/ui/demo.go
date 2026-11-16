package ui

import "time"

// SeedDemo fills a store with traffic that looks like a real debugging session,
// so the layout can be judged without a proxy behind it.
//
// The layout tests are its only caller: it fills the rows a rendering test draws.
func SeedDemo(s *Store) {
	now := time.Now()
	type row struct {
		method, url string
		status      int
		out, in     int64
		dur, ttfb   float64
		source      string
		notes       []Note
	}
	rows := []row{
		{"POST", "https://api.example.com/v1/login", 0, 0, 0, 0, 0, "", nil},
		{"GET", "https://api.example.com/v1/users/me?slim=1", 200, 1229, 0, 86.2, 77.1, "proxied",
			[]Note{
				{At: now, Rule: "token 打码", Kind: "rewrite", Text: "replace-body 已把 token 打码"},
				{At: now, Kind: "proxy", Text: "上游 gzip，已解码供查看，转发时按原编码重压"},
			}},
		{"GET", "https://api.example.com/v1/orders", 401, 312, 0, 41.3, 39.8, "proxied", nil},
		{"GET", "https://cdn.example.com/static/app.9f2c.js", 200, 188416, 0, 23.4, 21.1, "proxied", nil},
		{"GET", "https://cdn.example.com/static/logo.svg", 200, 2150, 0, 18.2, 17.0, "proxied", nil},
		{"POST", "https://api.example.com/v1/upload", 201, 0, 0, 0.2, 0, "mocked",
			[]Note{{At: now, Rule: "登录打桩", Kind: "mock", Text: "mock 201，源站未被访问"}}},
		{"PUT", "http://127.0.0.1:3000/graphql", 500, 864, 0, 1204, 1180, "proxied", nil},
		{"GET", "https://api.example.com/v1/auth/callback", 302, 0, 0, 12.6, 11.9, "proxied", nil},
		{"GET", "https://api.example.com/v1/config", 200, 4403, 0, 64.1, 60.2, "proxied", nil},
		{"DELETE", "https://api.example.com/v1/sessions/8821", 204, 0, 0, 31.8, 30.1, "proxied", nil},
		{"GET", "https://track.example.com/collect?v=1", 403, 0, 0, 0.1, 0, "aborted",
			[]Note{{At: now, Rule: "挡埋点", Kind: "abort", Text: "abort 403"}}},
		{"GET", "https://api.example.com/v1/health", 200, 42, 0, 8.7, 8.1, "proxied", nil},
	}
	// Insert oldest first, so the newest ends up at the front of the list.
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		sess := Session{
			ID:         uint64(i + 1),
			Phase:      "complete",
			Source:     r.source,
			Method:     r.method,
			URL:        r.url,
			Proto:      "HTTP/1.1",
			Status:     r.status,
			DurationMS: r.dur,
			TTFBMS:     r.ttfb,
			DNSMS:      0.7,
			ConnectMS:  1.1,
			TLSMS:      38.4,
			BytesOut:   r.out,
			BytesIn:    r.in,
			Started:    now.Add(-time.Duration(i) * time.Second),
			Notes:      r.notes,
		}
		if r.status == 0 {
			sess.Phase = "pending"
		}
		sess.Host = hostOf(sess.URL)
		s.Upsert(sess)
	}
	s.SetCounts(len(rows), 0)
}
