package ui_test

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The prompt for an assistant is the window's link to the rest of the user's
// tools, so these tests hold what makes it useful: the address is the one this
// core answers on, and it sends the reader to the index rather than repeating it.

func TestPromptNamesTheCoresAddress(t *testing.T) {
	var copied string
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.APIAddr = "http://127.0.0.1:8898"
	app.OnCopyPrompt = func(text string) { copied = text }
	tt := ui.NewTester(app.View, 1200, 700)
	tt.Frame()

	if err := tt.Click("复制给 AI 的提示词"); err != nil {
		t.Fatalf("click the prompt button: %v", err)
	}
	tt.Frame()
	if copied == "" {
		t.Fatal("the button copied nothing")
	}
	if !strings.Contains(copied, "http://127.0.0.1:8898") {
		t.Errorf("the prompt does not name the core's address:\n%s", copied)
	}
}

// TestPromptPointsAtTheIndex checks that the prompt sends the reader to /api. The
// index is generated from the running core with its own URLs filled in, so a
// prompt that repeated any of it would be a second copy to keep in step — and the
// one thing the assistant must read first is the page it did not get.
func TestPromptPointsAtTheIndex(t *testing.T) {
	var copied string
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.APIAddr = "http://127.0.0.1:8898"
	app.OnCopyPrompt = func(text string) { copied = text }
	tt := ui.NewTester(app.View, 1200, 700)
	tt.Frame()
	if err := tt.Click("复制给 AI 的提示词"); err != nil {
		t.Fatalf("click: %v", err)
	}
	tt.Frame()

	if !strings.Contains(copied, "http://127.0.0.1:8898/api") {
		t.Errorf("the prompt does not point at the index:\n%s", copied)
	}
	// It has to be short: a prompt that lists the whole API is a document the
	// user has to maintain, and the index already is that document.
	if len(copied) > 900 {
		t.Errorf("the prompt is %d bytes; it should be a pointer, not a manual", len(copied))
	}
	// It names the certificate, which is the one thing that silently breaks
	// HTTPS capture: without it every request looks like a CONNECT to a host the
	// proxy cannot read, and nothing says so.
	if !strings.Contains(copied, "certificate") {
		t.Errorf("the prompt never mentions the root certificate:\n%s", copied)
	}
}

// TestPromptButtonWaitsForTheAddress checks that the button is not built before
// the core says where its API is. A prompt with an empty address is worse than no
// button: it is a wrong address an assistant would use confidently.
func TestPromptButtonWaitsForTheAddress(t *testing.T) {
	app, _, _ := newTester(t, "https://api.test/v1/x")
	app.APIAddr = ""
	tt := ui.NewTester(app.View, 1200, 700)
	tt.Frame()
	if tt.HasText("复制给 AI 的提示词") {
		t.Error("the prompt button is drawn with no address to put in the prompt")
	}

	app.APIAddr = "http://127.0.0.1:8898"
	tt.Frame()
	if !tt.HasText("复制给 AI 的提示词") {
		t.Error("the prompt button did not appear once the core named its address")
	}
}
