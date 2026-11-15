package ui_test

import (
	"context"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	qqx "github.com/jlvihv/qingqiuxia/internal/ui"
)

// TestLiveCore pulls a running core's traffic through the real client and draws
// the window with it.
//
// It needs a core: QQX_API is its API address, and QQX_RENDER the file to write.
func TestLiveCore(t *testing.T) {
	apiAddr := os.Getenv("QQX_API")
	out := os.Getenv("QQX_RENDER")
	if apiAddr == "" || out == "" {
		t.Skip("set QQX_API and QQX_RENDER to render traffic from a running core")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	store := qqx.NewStore(1000)
	client := qqx.NewClient(apiAddr, "")
	st, err := client.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	sessions, total, err := client.Sessions(ctx, -1)
	if err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if total == 0 {
		t.Skip("the core holds no traffic")
	}
	// The API lists newest first; the store inserts at the front.
	for i := len(sessions) - 1; i >= 0; i-- {
		store.Upsert(sessions[i])
	}
	total2, dropped, err := client.Counts(ctx)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	store.SetCounts(total2, dropped)
	t.Logf("loaded %d sessions from %s (%s)", len(sessions), apiAddr, st.Version)

	app := qqx.NewApp(store)
	app.SetCoreStatus(st)
	app.CAInstalled = true
	// Choose the first row that has a response body, so the detail panel has
	// something to show: a 304 or a failed exchange has none.
	for i := 0; i < store.Len(); i++ {
		if sess, ok := store.At(i); ok && sess.BytesOut > 0 {
			app.Selected = i
			break
		}
	}

	img := ui.Render(app.View, 1440, 900, 1)
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
