# VibeBuddy Windows — 线协议（wire protocol）

本文档描述 iPhone App（VibeBuddy: Agent Monitor）与电脑端守护进程之间的线协议，
整理自上游仓库 `semantic-craft/iOS-vibebuddy` 的源码
（`VibeBuddyMac/Sources/VibeBuddyMacCore/VibeBuddyServer.swift`、
`VibeBuddyKit/Sources/VibeBuddyKit/Models.swift`、`VibeBuddyApp/`）。
Windows 版守护进程必须原样实现这套协议，iPhone App 不做任何版本/身份校验，
`macName` 仅用于界面显示。

## 传输基础

- 默认端口 **9876**（`VIBEBUDDY_PORT` 可覆盖）。局域网内明文 `http://host:9876/...`，
  WebSocket 为 `ws://host:9876/ws`。TLS 只用于 Cloudflare 远端场景，Windows 局域网版不需要。
- 鉴权：除 `/health` 外所有路由都要求 `Authorization: Bearer <token>` 请求头。
  服务端做精确字符串比对 `"Bearer <token>"`，空 token 永不通过；失败返回 **401**。
- Hook 路由（`/hook`、`/approval`、`/terminal`、`/statusline` 等）额外允许
  `?token=` 查询参数传 token（给不能设置 header 的 hook 用）。
- WS 升级握手走同样的 header-only 校验；失败时客户端看到的是 HTTP 400，
  此时手机会再调 `GET /snapshot`，只有返回 401/403 才判定认证失败。
- 日期格式：全部为 Unix 时间戳**秒数 Double**（Swift 默认 `.secondsSince1970`），不要用 ISO8601。
- 枚举 wire 值全部 camelCase 原样，大小写敏感：`claudeCode`、`needsResponse`、`allowSession`、`grokBot`……

## 配对流程

1. 电脑端生成配对二维码，内容是 `PairingPayload` 的原始 JSON：
   `{"host":"192.168.x.x","port":9876,"token":"<bearer>","macName":"MyPC"}`
   （Windows 下 host 填本机局域网 IPv4，macName 可填电脑名）。
2. iPhone 扫码后调 `GET /health`（唯一免认证，返回纯文本 `ok`）做可达性探测。
3. `POST /device`（需认证）：body 为 `DeviceRegistrationPayload` JSON，
   字段含 `token?`（APNs token）、`deviceID?`、`name?`、`model?`、`systemVersion?` 等；
   服务端也接受裸字符串 body 当作 APNs token。返回 200 即注册成功。
   Windows 版可只存 deviceID+name 做展示，但路由必须存在并返回 200。

## WebSocket `/ws`

- 连接：`ws://host:9876/ws?quotaProviders=codex,claude,grok,cursor,grokBot,antigravity`，
  `Authorization: Bearer` 头认证。
- 服务端行为：连接建立后先推一条当前全量 snapshot，之后每次状态变化再推；
  客户端 30s 无帧则发 ping，ping 10s 无回应则断线重连。
- 帧格式：文本帧，JSON 编码的 `ServerEvent`：
  - `{"snapshot": {…Snapshot…}}` —— 全量快照
  - `{"sessionUpdated": {…AgentSession…}}` —— 单个 session 增量
  - `{"sessionRemoved": {"id": "…"}}` —— session 移除
- `quotaProviders` 查询参数用于过滤 `snapshot.providerQuota`；初版可忽略，直接返回全部。

## Snapshot JSON 结构

顶层 `Snapshot` 关键字段：`sourceID?`（电脑端稳定身份，建议稳定的随机 UUID）、
`sessions: [AgentSession]`、`serverTime`（Double 秒）、`providerQuota?` 等，其余全可选。

`AgentSession` 核心字段（全部 camelCase）：
`id`、`agent`（`claudeCode|codex|qwen|kimi|antigravity|grok|grokBot|opencode|copilot|cursor`）、
`project`、`status`（`needsResponse|working|done`）、`statusSince`、`updatedAt`、
`hasUnreadCompletion`（必填）、`waitKind?`（`permission|question`）、
`pendingApproval?`、`pendingQuestion?`。其余 30+ 字段全部可选，缺失不影响手机渲染。

`pendingApproval`: `{id, tool, commandPreview, command?, filePath?, oldText?, newText?, permissionMode?, suggestedRule?, answerable?, allowsPersistentDecision?}`
`pendingQuestion`: `{id, prompt, options:[{id,label,value,description?}], questions?, isBlocking?, expiresAt?, answerable?}`

## POST /decision（审批）

- 请求：`{"approvalId": "…", "decision": "allow|deny|alwaysAllow|allowSession", "requestId": "…"}`
- `requestId` 是幂等键：重复请求必须返回与第一次相同的结果，不能二次决议。
- 响应无 body，只看 HTTP 状态：`200` 已接受 / `409` 审批已消失 / `503` 仍在处理中。

## POST /answer（回答问题 / 停止）

- 请求：`sessionId`（必填）、`intent`（`answer|steer|continue|stop`）、`requestId`（必填，幂等键）、
  `expectedQuestionId?`、`expectedStatusSince?`、`answer?`、`answers?`（`{questionId: [labels]}`）、`questionId?`
- 响应 JSON：`{"requestId":"…","status":"accepted|unknown|refused|failed","error":"…"}`；
  accepted→200，unknown→503，refused→409。

## 其他端点（全部 Bearer 认证）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/snapshot` | 全量快照（与 WS 首帧同构） |
| GET | `/task-read-capabilities` | 返回 `{"sourceID":"…","supported":[…]}`；404 则手机视为旧电脑降级 |
| GET | `/task-read?sourceID&sessionID&kind[&cursor]` | 读任务；MVP 可 404 |
| GET | `/history?sourceID&key&limit=30[&cursor]` | 历史对话；MVP 可 404 |
| POST | `/acknowledge` | `{sourceID, sessionID, completionID, markUnread?}` → `{"outcome":"accepted\|alreadyAcknowledged\|staleCompletion\|sourceMismatch\|unavailable"}` |
| POST | `/acknowledge-wait` | → 200/409 |
| POST | `/attention` | `{"sessionId":"…","attention":"followed\|normal\|muted"\|null}` |
| POST | `/jump` | `{"sessionId":"…"}` → `{"outcome":"…"}`；Windows 可返回 `{"outcome":"unsupported"}` |
| POST | `/dispatch` | `{agent, cwd, prompt, …}` → 200 `{"sessionId":"…"}` / 400 / 501 / 503 |
| GET | `/changes?sessionId&scope…` | 工作区 diff；MVP 可返回空结构 |
| GET | `/completion?sessionId&completionId` | 完整结果文本 |
| GET | `/recent-output?sessionId` | 最近输出 |
| POST | `/notified` | 手机回执，收下回 200 即可 |
| POST | `/activity` | LiveActivity token 注册，收下回 200 即可 |
| GET | `/connection-sync?deviceID` | 远端连接交接；无 proposal 时返回 204 |
| POST | `/connection-sync/receipt` | 交接回执 → 200/409 |

手机不经过电脑的：语音通话直连 OpenAI/Qwen/Doubao；手表经 WatchConnectivity 走 iPhone；
APNs 推送需要苹果开发者证书（Windows 版用本地 Toast/手机轮询代替）。

## Hook 端点（CLI → 守护进程）

- `POST /hook?agent=<source>` —— 事件上报，body 为 CLI hook 的原始 JSON。fail-open，
  超时 1–3 秒，任何失败都吞掉。source：`claude|codex|antigravity|grok|opencode|copilot|cursor`。
- `POST /approval?agent=<source>[&hold=<25–120>]` —— 阻塞式审批门。守护进程先查本地规则，
  未命中则生成待审批卡片并挂起 HTTP 响应等手机决定（默认 25s，可配到 120s）。
  决定后按各 agent 的格式回写：Claude 为
  `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`，
  Grok 为 `{"decision":…}`，Cursor 为 `{"permission":"allow"|"deny"}`。
- `POST /statusline[?agent=grok]` —— 状态行 JSON 上报（用量/上下文）。
- `POST /terminal` —— 终端定位信息（Windows 版重写为 Win32 窗口/进程枚举）。

## MVP 最小可用集

让 iPhone App 完整工作的最小集合：
`/health` + Bearer 校验 + `POST /device` + `GET /snapshot` + `WS /ws`
（首帧 snapshot + 变更推送）+ `POST /decision`（幂等）+ `POST /answer`（幂等）+
`POST /acknowledge*`。其余路由先返回 404/空结构，手机端均有降级路径。
