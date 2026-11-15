# 请求侠 · Qingqiuxia

一个用 Go 写的 API 调试核心：MITM 代理抓流量，规则改写、Mock、断点、重放，全部通过一个本地 HTTP API 观察和操作。

核心是**常驻守护进程**，没有窗口。GUI、脚本、浏览器都是它的客户端，所以同一份二进制既能给桌面应用当后端，也能在 CI 里跑。

## 能做什么

| 能力 | 说明 |
|---|---|
| 抓流量 | HTTP/1.1 + HTTPS（自带 CA，按域名动态签证书），记录方法、URL、头、body、DNS/TCP/TLS/TTFB 时序 |
| Mock | 匹配到的请求不发给源站，直接返回预设的状态码、头和 body |
| 改写 | 请求侧和响应侧共 9 种动作：`set-url` `set-query` `set-header` `remove-header` `set-body` `replace-body` `set-status` `redirect` `delay` |
| 断点 | 按规则暂停请求或响应，通过 API 看、改、放行或中止 |
| 重放 | 把抓到的请求再发出去，支持次数、延迟、间隔；重放同样走规则和断点 |
| 实时推送 | SSE 推流量变化和断点暂停事件 |

## 快速开始

```sh
go build -o qingqiuxia ./cmd/qingqiuxia
./qingqiuxia                 # 核心 + 窗口
./qingqiuxia -headless       # 只跑核心，API 在 127.0.0.1:8898
```

首次启动会在 `~/.config/qingqiuxia/` 生成 CA。让客户端信任它，就能看到 HTTPS 明文：

```sh
# curl
curl --proxy http://127.0.0.1:8899 --cacert ~/.config/qingqiuxia/ca.crt https://example.com/

# 或者下载后装进系统信任库
curl -O http://127.0.0.1:8898/api/certificate?download=1
```

然后看抓到了什么：

```sh
# API 只在 -headless 时存在：窗口模式是进程内直接调用，没有端口
curl -s 127.0.0.1:8898/api/sessions | jq '.sessions[0]'
```

## 命令行

```
-proxy    代理地址；空表示启动时不监听，由窗口里选端口
-headless 只跑核心，不开窗口，同时启用 HTTP API
-api      -headless 时的 API 地址（默认 127.0.0.1:8898）
-data     CA 和规则文件所在目录（默认 ~/.config/qingqiuxia）
-token    API 令牌，默认空（只在回环地址或 socket 上安全）
-max-body 每个 body 保留多少字节用于查看和改写（默认 4 MiB）
-sessions 内存里保留多少条会话（默认 2000）
-intercept 是否解密 TLS（默认 true）
-pprof    若设置，在这个地址上开 pprof 和 expvar
-v        打印每条连接的错误
```

## 给 AI 用

这个 API 的调用方经常是 AI：它拿着 shell 和 curl，要回答「这个请求为什么 500」。
所以两件事决定了一切 —— **每次调用花多少 token**，以及**第一次怎么知道能干什么**。

```sh
curl -s localhost:8898/api          # 入门页：可执行的调用序列，URL 已填好
```

返回纯文本，不是 HTML 也不是 schema 导出，短到一次读完。示例里的地址是真实
地址，可以直接复制粘贴。

### 要得少，才用得起

一次查询的开销差 46 倍：

```sh
# 找有没有 5xx —— 195 字节，约 50 tokens
curl -s 'localhost:8898/api/sessions?status=5xx&shape=table'

# 不加过滤拉 200 条 —— 9144 字节，约 2600 tokens
curl -s 'localhost:8898/api/sessions?limit=200'
```

| 参数 | 作用 |
|---|---|
| `limit=20` | 行数上限（默认 20，最大 1000） |
| `since=30s\|5m\|1h` | 只看最近的窗口 |
| `host=api.foo.com` | 域名，支持 `*.foo.com` |
| `status=5xx` | 状态类、单码，或 `200,201` |
| `method=POST` | 方法 |
| `url=*/login*` | URL 通配；纯字符串是子串匹配，不用写通配符 |
| `shape=table` | 数组代替对象，**省 60% 字节** |
| `fields=id,status,url` | 只要这些列 |

一条会话的详情也分档，因为"为什么 500"要的是摘要和响应体，不是请求头：

| 参数 | 大小 |
|---|---|
| `part=summary` | 约 230 字节 |
| `part=headers` | 只有头 |
| `part=body` | 有 body |
| `max=2000` | body 截断（会标 `truncated`，不静默丢） |

## API

全部 JSON，`/api/events` 是 SSE。`apiVersion` 目前是 1。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/status` | 版本、代理地址、会话数、规则数、暂停数 |
| GET | `/api/sessions?offset=&limit=` | 流量列表（新的在前，不含 body） |
| GET | `/api/sessions/{id}` | 单条会话详情，含请求/响应头和 body |
| DELETE | `/api/sessions` | 清空 |
| GET | `/api/events` | SSE：`session` / `dropped` / `paused` / `resumed` |
| POST | `/api/sessions/{id}/repeat` | 重放，body 可带 `count` `delay` `interval` |
| GET/POST | `/api/rules` | 列出 / 新增规则 |
| PUT | `/api/rules` | 整体替换（用于排序） |
| PATCH | `/api/rules/{id}` | 启停：`{"enabled":false}` |
| DELETE | `/api/rules/{id}` | 删除 |
| GET | `/api/rules` | 规则列表 |
| GET/PUT | `/api/breakpoints` | 断点规则 |
| GET | `/api/pauses` | 当前暂停的消息 |
| PUT | `/api/pauses/{id}` | 编辑暂停的消息：`side` `headers` `body` `status` |
| POST | `/api/pauses/{id}/resume` | 放行 |
| POST | `/api/pauses/{id}/abort` | 中止 |
| GET | `/api/certificate?download=1` | 下载 CA |

### 规则示例

Mock 一个登录接口：

```sh
curl -X POST 127.0.0.1:8898/api/rules -H 'Content-Type: application/json' -d '{
  "name": "fake login",
  "kind": "mock",
  "phase": "request",
  "pattern": "*/v1/login",
  "mock": {"status": 200, "body": "{\"token\":\"mocked\"}", "contentType": "application/json"}
}'
```

把响应里的 token 打码（gzip 的 body 会先解压再压回去，`Content-Encoding` 保持不变）：

```sh
curl -X POST 127.0.0.1:8898/api/rules -H 'Content-Type: application/json' -d '{
  "name": "scrub token",
  "kind": "rewrite",
  "phase": "response",
  "pattern": "https://api.example.com/*",
  "actions": [{"type": "replace-body", "find": "\"token\":\"[^\"]+\"", "replace": "\"token\":\"***\""}]
}'
```

匹配语法（`pattern`）：

- `example.com` —— 这个域及其子域，http 和 https 都算
- `https://api.example.com/v1/*` —— 限定协议、域名和路径
- `*` 匹配一段不含 `/` 的字符，`**` 跨 `/`，`?` 匹配一个字符
- 路径部分匹配 path，不含 query

## 结构

```
cmd/qingqiuxia       组装与启动
internal/msg         请求/响应的数据模型（与 net/http 解耦）
internal/proxy       代理本体：CONNECT、TLS 拦截、转发、抓取
internal/pipeline    Policy 实现：把规则、断点、会话串起来
internal/rule        匹配器、动作、规则引擎
internal/breakpoint  暂停、编辑、放行
internal/session     环形缓冲的会话存储 + 变更推送
internal/mitm        CA 与按域名签发的证书
internal/codec       gzip/deflate/br/zstd 编解码
internal/api         控制面 HTTP + SSE
internal/events      fan-out 事件总线
internal/httpx       hop-by-hop 头处理等小工具
```

依赖方向是单向的：`proxy` 只知道 `Policy` 接口，不知道规则和断点的存在；`pipeline` 是唯一同时知道三者（规则、断点、会话）的地方。所以换 UI、加脚本引擎、换存储都不会碰网络那一段。

## 验证

```sh
go test ./...            # 单元测试
go test ./... -race      # 竞争检测
go test ./internal/proxy -bench . -run XXX   # 基准
./scripts/e2e.sh         # 端到端：真实 HTTPS + curl + API 走一遍全部功能
```

`scripts/e2e.sh` 会真起一个核心，访问一个真实 HTTPS 站点，依次验证抓包、Mock、改写、断点暂停/编辑/放行、重放。

## 已知边界

- TLS 拦截目前只对客户端说 HTTP/1.1：隧道里跑的是一个 `http.Server`，不协商 h2。对源站可以用 h2。
- 上游证书默认不校验（和 Charles/mitmproxy 一样），因为要调的就是自签和过期证书的服务。
- body 超过 `-max-body` 的部分不保留：会照常转发给源站，但会话里只有开头一段，并标记 `truncated`。
- WebSocket 目前走透传，不解帧。

## 一个进程，直接调用

`qingqiuxia` 是核心，也是窗口，跑在一个进程里。窗口调用核心是 **Go 函数调用**，不经过网络：一条会话从代理记录下来到出现在表格里，中间没有 JSON 编码解码，也没有 socket。

边界靠接口表达，不靠传输层：

```go
// Backend 是窗口对核心的全部需求：六个操作。
// 桌面窗口用进程内实现调用它；脚本或浏览器用 HTTP 实现调用同一个接口。
type Backend interface {
	Status(ctx) (Status, error)
	Sessions(ctx, limit) ([]Session, int, uint64, error)
	SetCapture(ctx, on bool) error
	Pauses(ctx) ([]Pause, error)
	Resume(ctx, pauseID uint64, abort bool) error
	Watch(ctx, fn func(Change)) error
}
```

窗口不认识代理，只认识这六个方法。换网页前端时，实现同一个 `Backend` 即可。

### 端口：不预设，随时改

**启动时不监听任何端口**。窗口工具栏里有个端口输入框，填了才启动：

```
代理 [8899      ] [启动]
```

- 留空 = 让系统分配一个空闲端口
- 只填数字 = `127.0.0.1:<端口>`
- 填 `host:port` = 监听该地址
- 端口被占用时，错误显示在输入框旁边，不吞掉
- 运行中显示实际地址和 `[停止]`；停止后输入框保留上次的地址，改个数字再启动即可

命令行也可以直接给地址：`./qingqiuxia -proxy 127.0.0.1:8899`。

### 两种接口

核心对外有两个**互相独立**的接口，别把它们混为一谈：

| | 代理接口 | 控制接口 |
|---|---|---|
| 是什么 | HTTP 代理，转发真实流量 | HTTP API，回答状态、接受指令 |
| 谁连 | 被调试的客户端（浏览器 / App / curl） | GUI、脚本、浏览器 |
| 默认 | **不启动**，用户指定端口 | 随核心启动 |
| 生命周期 | 随时启停、随时换端口 | 核心活着就在 |

它们分开是因为服务和生命周期都不同：客户端在录制开关切换时一直连着代理；而脚本在代理**停着**的时候就要能调控制接口——那正是它最有用的时刻（启动代理、选端口、读上一次为什么失败）。把控制接口挂在代理的生命周期上，等于代理停着时就没法把它启动了。

```sh
# 控制接口：代理现在什么状态
curl -s 127.0.0.1:8898/api/proxy
# {"running":false}

# 通过控制接口启动代理，端口自己挑
curl -s -X POST 127.0.0.1:8898/api/proxy/start \
  -H 'Content-Type: application/json' -d '{"addr":"8899"}'
# {"running":true,"addr":"127.0.0.1:8899"}

# 端口被占时，错误会带回来，程序不退出
# {"error":"listen tcp 127.0.0.1:8899: bind: address already in use"}

# 停止，端口释放，控制接口仍在
curl -s -X POST 127.0.0.1:8898/api/proxy/stop
```

窗口模式下窗口自己直接调核心（进程内函数调用），控制接口同时存在——给脚本和浏览器用，不影响窗口。

### headless

`-headless` 不开窗口，适合没有显示器的机器或 CI：

```sh
./qingqiuxia -headless                        # 只有控制接口 + 代理
./qingqiuxia -headless -proxy 127.0.0.1:8899  # 顺带把代理也起来
```

没有显示器也能看界面，`ui.Render` 把它画成图片：

```sh
qqx_render=/tmp/window.png go test ./internal/ui -run TestRenderLayout
```

### 布局### 布局

三栏加两条可拖动的分隔：侧栏（184px，域名分组）、流量表（吃剩余宽度）、详情面板（452px）。工具栏在上，状态栏在下，断点暂停时两栏之间插一条横幅——用横幅而不是弹窗，因为暂停是高频动作，弹窗会挡住判断所需的列表。

```
┌ 工具栏：录制状态 · 代理地址 · CA 状态 ────── 流量｜规则｜断点｜证书 ┐
├ 断点横幅（仅暂停时）─────────────────────────────────────┤
│ 侧栏 │ 流量表                      │ 详情：概览｜请求｜响应｜说明｜时序 │
├ 状态栏：会话 · 丢弃 · 核心内存/线程 · API 版本 ────────────────┤
```

`说明` 那一页是核心 `session.Note` 的呈现：讲清这个请求为什么长这样，哪条规则改了什么、上游是不是 gzip、转发时有没有重新压缩。

### 视觉规则

所有颜色、圆角、间距、字号都在 `internal/ui/theme.go` 的 token 里，builder 不写死任何值。几条约束：

- **颜色只承载语义**：2xx 绿、3xx 蓝、4xx 黄、5xx 红；GET 绿、POST 黄、DELETE 红。这是抓包工具的通用语言。
- **用亮度分层，不靠边框**：窗口 `#0e1014`，面板 `#15181e`，卡片 `#1b1f27`。边框只有 `#14171d`，在近黑背景上几乎不可见——一条亮线会把视线从数据上拉走。
- **选中行是克制的深蓝**（`#1d3a6b`），不是强调色本身：一行满蓝在一晚上要读几千行的日志里太累，而且状态码的绿红得留在上面。
- **所有数据等宽**，无一例外。数字列对齐才能扫出异常的耗时。
- 字号 9.5/10.5/11/11.5/12/12.5/13，间距 4/6/8/12/16，圆角 3/5/6。没有中间值。

界面的 theme 通过 `c.SetTheme` 注入，所以输入框、滚动条、焦点环和分隔条跟面板用同一套色；只覆盖部分字段会让框架的默认灰漏进来——输入框会比面板更亮，就是这个原因。
