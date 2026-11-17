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
// It describes the tool in the third person, as a piece of software, rather than
// as something the reader is doing: an assistant reading "I am debugging" would
// take the user's role and answer in their voice, when what it needs is to know
// what the tool is and where to read about it.
//
// The base URL is filled in, because an assistant that has to work out the port
// will get it wrong, and the port changes every run in window mode.
func promptText(base string) string {
	return fmt.Sprintf(`qingqiuxia 是一个 HTTP/HTTPS 调试代理：抓流量、按规则改写、Mock、
断点暂停，都通过一个本地控制接口操作，也可以当 API 用。

控制接口：%s

帮助在 %s/api —— 一页纯文本，讲清能做什么、每个调用花多少 token、
怎么在服务端过滤。先读它，不要直接拉全量列表。

代理（被调试程序连的那个）不一定在跑：
  %s/api/status          看状态
  %s/api/proxy/start     启动它

要解密 HTTPS，客户端必须先信任它的根证书：
  %s/api/certificate?download=1

把这份证书装进系统信任库，或给 curl 用 --cacert。不做这一步，HTTPS 流量
在列表里只有一条 CONNECT，里面全是密文。`,
		base, base, base, base, base)
}
