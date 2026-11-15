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
./qingqiuxia                 # 代理 127.0.0.1:8899，API 127.0.0.1:8898
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
curl -s 127.0.0.1:8898/api/sessions | jq '.sessions[0]'
```

## 命令行

```
-proxy   代理监听地址（默认 127.0.0.1:8899）
-api     API 监听地址（默认 127.0.0.1:8898）
-data    CA 和规则文件所在目录（默认 ~/.config/qingqiuxia）
-token   API 令牌，默认空（只在回环地址上安全）
-max-body 每个 body 保留多少字节用于查看和改写（默认 4 MiB）
-sessions 内存里保留多少条会话（默认 2000）
-intercept 是否解密 TLS（默认 true）
-v       打印每条连接的错误
```

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
