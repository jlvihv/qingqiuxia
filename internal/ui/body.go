package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The body pane.
//
// It shows, it does not edit: what a debugging session needs from a body is to
// read it, copy a value out of it, and see whether it is what the client got.
// A read-only text area gives all three and costs nothing, where an editor
// brings a language server, folds and diagnostics that nobody asked this panel
// for.
//
// The body being long is the normal case: a gzipped JSON response of a few
// hundred kilobytes is a few thousand lines. TextArea lays out only the lines in
// view, so the pane scrolls without the list of a whole body in memory being a
// problem.

// bodyPane draws one side's headers and body.
func (a *App) bodyPane(c *ui.Context, p *Palette, m *Message, side string) {
	if m == nil {
		ui.Text(c, "这个"+side+"没有内容").FontSize(tokenFontBody).
			TextColor(p.TextMuted).Padding(tokenSpace4, 10, 0, 0)
		return
	}

	a.section(c, p, side+"头", true)
	if len(m.Headers) == 0 {
		ui.Text(c, "（无）").FontSize(tokenFontData).TextColor(p.Dim).
			Padding(0, 10, tokenSpace1, 0)
	}
	for _, h := range m.Headers {
		ui.Row(c).FillWidth().Padding(0, 10, 1, 0).Gap(tokenSpace3).Children(func() {
			ui.Text(c, h.Name).FontSize(tokenFontData).Font(monoFamily).
				TextColor(p.Info).Width(150).SingleLine()
			ui.Text(c, h.Value).FontSize(tokenFontData).Font(monoFamily).
				TextColor(p.Text).Grow(1).SingleLine()
		})
	}

	a.section(c, p, side+"体", false)
	switch {
	case m.Streaming:
		a.bodyNotice(c, p, "这是流式响应（如 SSE），核心边转发边记录，不缓冲完整内容。")
	case m.Binary:
		a.bodyNotice(c, p, fmt.Sprintf("二进制内容，%d 字节，不按文本显示。", m.Size))
	case m.Body == "":
		a.bodyNotice(c, p, "（空）")
	default:
		a.bodyText(c, p, m, side)
	}
	if m.Truncated {
		a.bodyNotice(c, p, "内容超过抓取上限，只保留了开头部分。")
	}
}

// bodyNotice says something about a body instead of drawing it.
func (a *App) bodyNotice(c *ui.Context, p *Palette, text string) {
	ui.Text(c, text).FontSize(tokenFontData).TextColor(p.TextMuted).
		Padding(0, 10, tokenSpace1, 0)
}

// bodyText draws a text body, monospaced and selectable but not editable.
func (a *App) bodyText(c *ui.Context, p *Palette, m *Message, side string) {
	// The size and the first line tell a reader what they are looking at before
	// they scroll: a pretty-printed JSON and a single minified line read very
	// differently at the same byte count.
	lines := strings.Count(m.Body, "\n") + 1
	meta := fmt.Sprintf("%d 行 · %s", lines, formatBytes(m.Size))
	if ct := m.ContentType; ct != "" {
		meta = ct + " · " + meta
	}
	ui.Text(c, meta).FontSize(tokenFontSmall).TextColor(p.Dim).
		Padding(0, 10, tokenSpace2, 0)

	// One text area per body, keyed by the session it belongs to, so that
	// switching rows starts fresh rather than carrying the previous body's
	// scroll position. The key is given to the context before the widget is
	// built: a widget's state is made as it is constructed, and a key set
	// afterwards would arrive too late, which the framework says so plainly.
	value := m.Body
	c.Key(fmt.Sprintf("body-%d-%s", a.detailKey, side))
	ui.TextArea(c, &value).
		ReadOnly(true).
		Font(monoFamily).
		FontSize(tokenFontData).
		FillWidth().
		MinHeight(200).
		MaxHeight(420)
}
