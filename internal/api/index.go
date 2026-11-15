package api

import "net/http"

// 这一页是写给刚被指向这个 API 的程序的，不是写给读网页的人的：一个拿着
// shell 和 curl 的 AI 要先知道能做什么，而它花在了解上的每个 token，都是从
// 回答问题上挪走的。
//
// 所以是纯文本，不是 HTML，也不是 schema 导出。短到一次读完，开头就是回答
// 大部分问题的那三个调用，并且说清每个调用花多少：200 行列表和十行过滤结果
// 在上下文里差 46 倍，不知道这一点的人会先要全部，然后被淹掉。

const indexText = `请求侠 (qingqiuxia) 控制接口

一个调试代理。它记录 HTTP 和 HTTPS 流量，也能自己应答请求（Mock）、改写
请求（改写），或者把请求拦下来等人决定（断点）。

读完这页就开始用：下面每条路径返回的都是 JSON，除非特别说明。

== 回答大部分问题的三个调用 ==

  # 已经抓到了什么，最新的在前
  curl -s 'API/api/sessions?limit=20&shape=table'

  # 一次请求的摘要：url、状态、各段耗时，约 230 字节
  curl -s 'API/api/sessions/ID?part=summary'

  # 它跑着吗、在录吗、代理在哪
  curl -s 'API/api/status'

== 有意识地少要一点 ==

一行 JSON 大约 300 字节，两百行就吃掉 AI 大半个上下文。所以过滤和格式都在
服务端做：

  limit=20         最多几行（默认 20，上限 1000）
  since=30s|5m|1h  只看最近这段时间
  host=api.foo.com 或 *.foo.com
  status=5xx       或 404，或 2xx，或单个状态码，或 200,201
  method=POST
  url=*/login*     * 匹配任意字符，? 匹配一个；纯字符串按子串匹配
  shape=table      用数组代替对象，省约 60% 字节
  fields=id,status,url   只要这几列

一次请求的详情分成三块，因为问"为什么 500"时通常不关心请求头：

  不带 part=       全部：头加 body
  part=summary     url、状态、各段耗时、大小    约 230 字节
  part=headers     只有请求和响应的头
  part=body        只有 body，已解码，可以直接读
  max=2000         每个 body 最多取多少字节

== 改变流量 ==

规则按 URL 模式匹配，按顺序执行。一条规则是三种之一：

  mock     不通源站，直接返回
  rewrite  改完再转发
  abort    直接拒绝

  # 一个 Mock：路径以 /login 结尾的请求都返回 200 和这个 body
  curl -s -X POST API/api/rules -H 'Content-Type: application/json' -d '{
    "name":"登录打桩","kind":"mock","phase":"request","pattern":"*/login",
    "mock":{"status":200,"body":"{\\"token\\":\\"假的\\"}","contentType":"application/json"}}'

  # 一个改写：把响应里所有的 secret 换成 ***
  curl -s -X POST API/api/rules -H 'Content-Type: application/json' -d '{
    "name":"打码","kind":"rewrite","phase":"response","pattern":"*",
    "actions":[{"type":"replace-body","find":"secret","replace":"***"}]}'

  GET    API/api/rules            列出全部
  PATCH  API/api/rules/ID         {"enabled":false} 停用某条
  DELETE API/api/rules/ID         删除

改写动作：set-url、set-query、set-header、remove-header、set-body、
replace-body、set-status、redirect。

== 截住流量 ==

断点规则把匹配到的流量停下来，等人决定：

  curl -s -X PUT API/api/breakpoints -H 'Content-Type: application/json' \
    -d '{"breakpoints":[{"name":"登录","phase":"request","pattern":"*/login"}]}'

  curl -s API/api/pauses                       现在停着哪些
  curl -s -X PUT API/api/pauses/ID \
    -H 'Content-Type: application/json' \
    -d '{"headers":{"Authorization":["Bearer test"]}}'
  curl -s -X POST API/api/pauses/ID/resume     放行
  curl -s -X POST API/api/pauses/ID/abort      拒绝

被拦下的请求会一直占着客户端，所以记得放行：没人处理的断点五分钟后超时。

== 代理本身 ==

  curl -s API/api/proxy                        在监听吗，在哪
  curl -s -X POST API/api/proxy/start \
    -H 'Content-Type: application/json' -d '{"addr":"0.0.0.0:8899"}'
  curl -s -X POST API/api/proxy/stop

  代理和这个控制接口是两个不同的监听。代理是给被调试的客户端连的，这个接口
  是给工具连的。代理可以停掉，这个接口照样回答。

== 持续观察，而不是反复问 ==

  curl -sN API/api/events

服务端推送事件：每行一个 JSON，事件名是 session、dropped、paused 或 resumed。
连接不断就一直推。

== 出错的时候 ==

{"error":"..."} 加一个状态码：400 参数不对，404 没有这个东西，409 冲突
（比如端口被占了），501 这个核心没有这个功能。错误信息会说清哪里不对，
照着改，不要原样重试。
`

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	body := indexText
	if base := s.baseURL(r); base != "" {
		body = replaceAll(body, "API", base)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Qingqiuxia-Api-Version", itoa(APIVersion))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// baseURL is how a caller reaches this API, so that the examples can be
// pasted as they are: an agent that has to rewrite every URL will get one of
// them wrong.
func (s *Server) baseURL(r *http.Request) string {
	if s.base != "" {
		return s.base
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if host := r.Host; host != "" {
		return scheme + "://" + host
	}
	return ""
}

// replaceAll is strings.ReplaceAll without the import in this file.
func replaceAll(s, old, new string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out = append(out, new...)
			i += len(old)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}

// itoa is strconv.Itoa without the import.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
