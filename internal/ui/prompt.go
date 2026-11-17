package ui

import "fmt"

// promptText is what a user copies for an AI assistant.
//
// It is short on purpose. The API has an index page written for exactly this
// reader — the three calls that answer most questions, what each costs in tokens,
// how to filter server-side — and it is generated from the running core, so its
// URLs are the ones that work. Repeating any of that here would be a second copy
// to keep in step; what the prompt has to do is get the assistant to fetch the
// first one, and to say enough that it knows why it is fetching it.
//
// The base URL is filled in, because an assistant that has to work out the port
// will get it wrong, and the port changes every run in window mode.
func promptText(base string) string {
	return fmt.Sprintf(`我在调试一个 HTTP 接口，用 qingqiuxia 抓流量。它的控制接口在 %s。

先读一遍 %s/api —— 那是一页纯文本的入门说明：能做什么、每个调用花多少 token、
怎么在服务端过滤。读完再动手，不要直接拉全量列表。

代理没启动的话，用 POST %s/api/proxy/start 启动它（body 可以是 {"addr":"8899"}）。
代理在 %s/api/status 里能看到地址。

我的问题是：`, base, base, base, base)
}
