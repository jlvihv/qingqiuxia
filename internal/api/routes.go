package api

import (
	"fmt"
	"net/http"
)

// 这张表是接口的唯一来源：路由从它注册，清单从它生成。写成两处必然有一天对
// 不上，而对不上时最吃亏的是照着清单调用的程序 —— 它会以为某个功能不存在，
// 或者以为参数是清单里写的那样。
//
// 每一项都写清「干什么」「要什么」「给什么」，因为读它的往往是没读过源码的
// 程序：一个只有路径和方法的清单，它还得再猜一遍参数。

// route 是一个端点。
type route struct {
	// Method and Path are the route, as net/http matches them.
	Method, Path string
	// Group orders the list: what a caller usually wants first.
	Group string
	// Summary is one line: what it does.
	Summary string
	// Params names the query parameters it takes, and Body says what a request
	// body is, if it takes one.
	Params []param
	Body   string
	// Returns says what comes back, in a few words.
	Returns string
	// Handler names the method on Server that serves it. It is a name rather
	// than a function because the table is a package-level value, which cannot
	// refer to a Server.
	Handler string
}

// param is one query parameter.
//
// The tags matter: this goes straight to a caller that reads JSON, and a list
// of {"Name","Values","Help"} is not the shape anyone expects.
type param struct {
	Name   string `json:"name"`
	Values string `json:"values"`
	Help   string `json:"help"`
}

// routes is every endpoint, in the order the list shows them.
//
// The groups are the questions a caller arrives with: what is happening, what
// was captured, how is it being changed, is anything waiting, and the proxy
// itself.
var routes = []route{
	// —— 它现在什么状态 ——
	{
		Group: "状态", Method: "GET", Path: "/api/status",
		Summary: "它跑着吗、在录吗、代理在哪、攒了多少会话",
		Returns: "{version, proxyAddr, capturing, sessions, rules, paused, memoryMb, goroutines}",
		Handler: "handleStatus",
	},
	{
		Group: "状态", Method: "GET", Path: "/api/proxy",
		Summary: "代理在监听吗，在哪（和这个控制接口是两个不同的监听）",
		Returns: "{running, addr}",
		Handler: "handleProxyStatus",
	},
	{
		Group: "状态", Method: "POST", Path: "/api/proxy/start",
		Summary: "启动代理，端口自己挑",
		Body:    `{"addr":"0.0.0.0:8899"}，只写 "8899" 表示 127.0.0.1:8899，留空表示随机空闲端口`,
		Returns: "{running, addr}；端口被占时回 409 并说明原因",
		Handler: "handleProxyStart",
	},
	{
		Group: "状态", Method: "POST", Path: "/api/proxy/stop",
		Summary: "停掉代理，释放端口",
		Returns: "{running:false}",
		Handler: "handleProxyStop",
	},
	{
		Group: "状态", Method: "POST", Path: "/api/capture",
		Summary: "开/关录制：关掉后代理照常转发，只是不再记录新会话",
		Body:    `{"enabled":true|false}`,
		Returns: "{capturing}",
		Handler: "handleCapture",
	},
	{
		Group: "状态", Method: "GET", Path: "/api/certificate",
		Summary: "取 CA 证书，装到被调试的设备上才能解密 HTTPS",
		Params:  []param{{"download", "任意值", "下载 PEM 文件；不带时回 JSON"}},
		Handler: "handleCertificate",
	},

	// —— 抓到了什么 ——
	{
		Group: "流量", Method: "GET", Path: "/api/sessions",
		Summary: "已经抓到的流量，最新的在前。过滤和格式都在服务端做",
		Params: []param{
			{"limit", "20", "最多几行，默认 20，上限 1000"},
			{"since", "30s|5m|1h", "只看最近这段时间"},
			{"host", "api.foo.com|*.foo.com", "按域名，可用通配符"},
			{"status", "5xx|404|200,201", "按状态类或具体码"},
			{"method", "POST", "按方法"},
			{"url", "*/login*|/v1/", "按 URL，可用通配符；纯字符串按子串匹配"},
			{"shape", "table", "用数组代替对象，省约 60% 字节"},
			{"fields", "id,status,url", "只要这几列"},
			{"offset", "0", "跳过前几行"},
		},
		Returns: "shape=table 时 {cols,rows,total,dropped}；否则 {sessions,total,held,dropped}",
		Handler: "handleListSessions",
	},
	{
		Group: "流量", Method: "GET", Path: "/api/sessions/{id}",
		Summary: "一次请求的详情，分块取，别一次要全部",
		Params: []param{
			{"part", "summary|headers|body", "只要其中一块；不带就是全部（头加 body）"},
			{"max", "2000", "每个 body 最多取多少字节，截断时会标 truncated"},
		},
		Returns: "session 对象；part=summary 约 230 字节，全部约 3.3 KB",
		Handler: "handleGetSession",
	},
	{
		Group: "流量", Method: "DELETE", Path: "/api/sessions",
		Summary: "清空已抓到的流量（id 不复用，旧的引用不会误指新会话）",
		Returns: "{cleared}",
		Handler: "handleClearSessions",
	},
	{
		Group: "流量", Method: "POST", Path: "/api/sessions/{id}/repeat",
		Summary: "把抓到的那条请求再发一次，同样走规则和断点",
		Body:    `{"count":1,"delay":0,"interval":0}，毫秒；count 上限 100`,
		Returns: "{queued}，202：重放在后台跑",
		Handler: "handleRepeat",
	},
	{
		Group: "流量", Method: "GET", Path: "/api/events",
		Summary: "持续推送变化，不用反复轮询。SSE，连接不断就一直推",
		Returns: "每行一个 JSON，事件名 session|dropped|paused|resumed",
		Handler: "handleEvents",
	},

	// —— 怎么改流量 ——
	{
		Group: "规则", Method: "GET", Path: "/api/rules",
		Summary: "列出规则。每条同时给 actions（能原样读回）和 effects（一句人话）",
		Returns: "{rules:[{id,name,enabled,kind,phase,pattern,actions,mock,effects}]}",
		Handler: "handleListRules",
	},
	{
		Group: "规则", Method: "GET", Path: "/api/rules/{id}",
		Summary: "看一条规则：要改之前先读它，改一个字段再 PUT 回去",
		Returns: "一条规则，字段同上",
		Handler: "handleGetRule",
	},
	{
		Group: "规则", Method: "POST", Path: "/api/rules",
		Summary: "新建一条规则：mock 直接返回、rewrite 改完转发、abort 拒绝",
		Body: `{"name","kind":"mock|rewrite|abort","phase":"request|response",
       "pattern":"*/login","actions":[...],"mock":{...},"delay":毫秒}
       改写动作：set-url set-query set-header remove-header set-body
                 replace-body set-status redirect`,
		Returns: "201 加新建的规则",
		Handler: "handleAddRule",
	},
	{
		Group: "规则", Method: "PUT", Path: "/api/rules",
		Summary: "整体替换（用来排序）",
		Body:    `{"rules":[...]}`,
		Returns: "替换后的列表",
		Handler: "handleReplaceRules",
	},
	{
		Group: "规则", Method: "PATCH", Path: "/api/rules/{id}",
		Summary: "启用或停用某条",
		Body:    `{"enabled":false}`,
		Returns: "改好的规则",
		Handler: "handlePatchRule",
	},
	{
		Group: "规则", Method: "DELETE", Path: "/api/rules/{id}",
		Summary: "删除某条",
		Returns: "204",
		Handler: "handleDeleteRule",
	},

	// —— 有没有停着的 ——
	{
		Group: "断点", Method: "GET", Path: "/api/breakpoints",
		Summary: "断点规则：匹配到的流量会停下来等人放行。和上面「规则」不是一回事",
		Returns: "{breakpoints:[{id,name,enabled,phase,pattern,readOnly}]}",
		Handler: "handleListBreakpoints",
	},
	{
		Group: "断点", Method: "PUT", Path: "/api/breakpoints",
		Summary: "设置断点规则（整体替换）",
		Body:    `{"breakpoints":[{"name","phase":"request|response","pattern","readOnly":false}]}`,
		Returns: "设置后的列表",
		Handler: "handleReplaceBreakpoints",
	},
	{
		Group: "断点", Method: "GET", Path: "/api/pauses",
		Summary: "现在停着哪些，等人决定的",
		Returns: "{pauses:[{id,sessionId,phase,ruleName,method,url,request,response}]}",
		Handler: "handleListPauses",
	},
	{
		Group: "断点", Method: "PUT", Path: "/api/pauses/{id}",
		Summary: "改一条停着的请求或响应，然后放行",
		Body:    `{"side":"request|response","headers":{"名字":["值"]},"body":"...","status":200}`,
		Returns: "{edited}",
		Handler: "handleEditPause",
	},
	{
		Group: "断点", Method: "POST", Path: "/api/pauses/{id}/resume",
		Summary: "放行（被拦着的客户端一直等着，别忘了解开）",
		Returns: "{resumed}",
		Handler: "handleResume",
	},
	{
		Group: "断点", Method: "POST", Path: "/api/pauses/{id}/abort",
		Summary: "拒绝",
		Returns: "{aborted}",
		Handler: "handleAbortPause",
	},

	// —— 说明书 ——
	{
		Group: "说明", Method: "GET", Path: "/api",
		Summary: "入门页：能做什么、怎么省 token、几种改流量的区别。人读这个",
		Returns: "纯文本",
		Handler: "handleIndex",
	},
	{
		Group: "说明", Method: "GET", Path: "/api/endpoints",
		Summary: "就是这一页：全部端点，机器可枚举",
		Params: []param{
			{"group", "规则", "只看某一组"},
			{"format", "json|text", "默认 json；text 是人读的表格"},
		},
		Handler: "handleEndpoints",
	},
}

// register wires the table's routes.
//
// The handler name is looked up here rather than stored as a function, so that
// the table stays a plain value and a missing name is a start-up panic rather
// than a route that answers nothing.
func (s *Server) register() {
	for _, r := range routes {
		h, ok := s.handlers()[r.Handler]
		if !ok {
			panic("api: route " + r.Method + " " + r.Path + " names no handler " + r.Handler)
		}
		s.mux.HandleFunc(r.Method+" "+r.Path, h)
	}
	// A second name for the index, which is the obvious thing to try.
	s.mux.HandleFunc("GET /api/help", s.handleIndex)
}

// handlers is every handler a route may name.
func (s *Server) handlers() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"handleStatus":             s.handleStatus,
		"handleCapture":            s.handleCapture,
		"handleCertificate":        s.handleCertificate,
		"handleListSessions":       s.handleListSessions,
		"handleGetSession":         s.handleGetSession,
		"handleClearSessions":      s.handleClearSessions,
		"handleRepeat":             s.handleRepeat,
		"handleEvents":             s.handleEvents,
		"handleListRules":          s.handleListRules,
		"handleGetRule":            s.handleGetRule,
		"handleAddRule":            s.handleAddRule,
		"handleReplaceRules":       s.handleReplaceRules,
		"handlePatchRule":          s.handlePatchRule,
		"handleDeleteRule":         s.handleDeleteRule,
		"handleListBreakpoints":    s.handleListBreakpoints,
		"handleReplaceBreakpoints": s.handleReplaceBreakpoints,
		"handleListPauses":         s.handleListPauses,
		"handleEditPause":          s.handleEditPause,
		"handleResume":             s.handleResume,
		"handleAbortPause":         s.handleAbortPause,
		"handleProxyStatus":        s.handleProxyStatus,
		"handleProxyStart":         s.handleProxyStart,
		"handleProxyStop":          s.handleProxyStop,
		"handleIndex":              s.handleIndex,
		"handleEndpoints":          s.handleEndpoints,
	}
}

// endpointsJSON is the list, as a program reads it.
type endpointsJSON struct {
	// APIVersion changes when a field changes meaning.
	APIVersion int `json:"apiVersion"`
	// Base is the address the caller reached, so its examples can be built.
	Base string `json:"base"`
	// Groups are the headings, in order.
	Groups []string `json:"groups"`
	// Endpoints is every route, each saying what it takes and gives.
	Endpoints []endpointJSON `json:"endpoints"`
}

// endpointJSON is one route.
type endpointJSON struct {
	Method  string  `json:"method"`
	Path    string  `json:"path"`
	Group   string  `json:"group"`
	Summary string  `json:"summary"`
	Params  []param `json:"params,omitempty"`
	Body    string  `json:"body,omitempty"`
	Returns string  `json:"returns,omitempty"`
	Example string  `json:"example,omitempty"`
}

// handleEndpoints answers with the whole table.
//
// This is what the index page is not: the index is prose, written to be read
// once, and it cannot list twenty-five routes without becoming a document
// nobody finishes. A program wants the list itself — every method, path,
// parameter and reply — and can filter it without reading a word of prose.
func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	group := q.Get("group")
	base := s.baseURL(r)

	var out endpointsJSON
	out.APIVersion = APIVersion
	out.Base = base
	seen := make(map[string]bool, 8)
	for _, rt := range routes {
		if group != "" && rt.Group != group {
			continue
		}
		if !seen[rt.Group] {
			seen[rt.Group] = true
			out.Groups = append(out.Groups, rt.Group)
		}
		ep := endpointJSON{
			Method: rt.Method, Path: rt.Path, Group: rt.Group,
			Summary: rt.Summary, Params: rt.Params, Body: rt.Body, Returns: rt.Returns,
		}
		ep.Example = exampleFor(rt, base)
		out.Endpoints = append(out.Endpoints, ep)
	}

	if q.Get("format") == "text" {
		writeEndpointsText(w, &out)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// exampleFor builds one curl command a caller can run as it stands.
func exampleFor(rt route, base string) string {
	url := base + rt.Path
	switch rt.Method {
	case "GET", "DELETE":
		return "curl -s" + methodFlag(rt.Method) + " '" + url + "'"
	}
	if rt.Body == "" {
		return "curl -s" + methodFlag(rt.Method) + " '" + url + "'"
	}
	return "curl -s" + methodFlag(rt.Method) + " '" + url + "' \\\n  -H 'Content-Type: application/json' -d '{...}'"
}

// methodFlag is the curl flag that names a method, or nothing for a GET.
func methodFlag(method string) string {
	switch method {
	case "GET":
		return ""
	case "DELETE":
		return " -X DELETE"
	}
	return " -X " + method
}

// writeEndpointsText writes the list as a table, for a person reading a
// terminal.
func writeEndpointsText(w http.ResponseWriter, out *endpointsJSON) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	bw := &textWriter{w: w}
	for _, g := range out.Groups {
		bw.printf("\n== %s ==\n", g)
		for _, ep := range out.Endpoints {
			if ep.Group != g {
				continue
			}
			bw.printf("%-6s %-28s %s\n", ep.Method, ep.Path, ep.Summary)
			for _, p := range ep.Params {
				bw.printf("       %-10s %-16s %s\n", p.Name, p.Values, p.Help)
			}
			if ep.Body != "" {
				bw.printf("       body  %s\n", firstLine(ep.Body))
			}
		}
	}
	bw.printf("\n共 %d 个端点。加 ?format=json 取机器可读的版本。\n", len(out.Endpoints))
}

// textWriter writes lines, ignoring the error a closed connection gives: the
// caller is gone and there is nobody to tell.
type textWriter struct{ w http.ResponseWriter }

func (t *textWriter) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(t.w, format, args...)
}

// firstLine is the first line of a multi-line string.
func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
