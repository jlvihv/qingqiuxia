package ui_test

import (
	"fmt"
	"testing"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// BenchmarkUpsert measures the cost of a arriving request at different list
// sizes. It must stay flat: the store appends, it does not shift.
func BenchmarkUpsert(b *testing.B) {
	for _, size := range []int{100, 2000, 5000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			s := qqx.NewStore(size)
			for i := 0; i < size; i++ {
				s.Upsert(qqx.Session{ID: uint64(i)})
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Upsert(qqx.Session{ID: uint64(size + i)})
			}
		})
	}
}

// BenchmarkVisible measures filtering a full list, which a frame does whenever
// the data changed.
func BenchmarkVisible(b *testing.B) {
	s := qqx.NewStore(5000)
	for i := 0; i < 5000; i++ {
		s.Upsert(qqx.Session{
			ID: uint64(i), Method: "GET", Status: 200,
			URL:  fmt.Sprintf("https://api%d.test/v1/items/%d", i%20, i),
			Host: fmt.Sprintf("api%d.test", i%20),
		})
	}
	app := qqx.NewApp(s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.Invalidate()
		app.Visible()
	}
}

// BenchmarkUpsertThenVisible is the real frame pattern: a session arrives, the
// store is invalidated, and the frame filters. It shows what a burst costs.
func BenchmarkUpsertThenVisible(b *testing.B) {
	s := qqx.NewStore(5000)
	for i := 0; i < 5000; i++ {
		s.Upsert(qqx.Session{ID: uint64(i), Method: "GET", Status: 200,
			URL: fmt.Sprintf("https://api%d.test/v1/x", i%20), Host: fmt.Sprintf("api%d.test", i%20)})
	}
	app := qqx.NewApp(s)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Upsert(qqx.Session{ID: uint64(10000 + i), Method: "GET", Status: 200,
			URL: "https://api.test/v1/x", Host: "api.test"})
		app.Invalidate()
		app.Visible()
	}
}
