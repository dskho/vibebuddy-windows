# vibebuddy-windows

Windows 版 [vibebuddy](https://github.com/semantic-craft/iOS-vibebuddy) 电脑端：
一个跑在你 Windows PC 上的本地守护进程，复刻 Mac 版 `vibebuddyd` 的线协议，
让 iPhone App（VibeBuddy: Agent Monitor）直接配对你的 PC，Apple Watch 经 iPhone
中转照常工作。

**Free & open source · 无账号 · 无埋点 · 你的 AI key 只进你自己的 provider**

## 架构

```
Claude Code / Codex / Grok Build / Cursor（Windows 版 CLI）
  │ hook 事件 (stdin JSON)
  ▼
hooks/*.ps1 ──POST http://127.0.0.1:9876/hook?agent=<src>──▶ vibebuddy-windows.exe
  (Bearer token, fail-open)                                   │
  │ /approval（阻塞，等手机）                                  │ 内存状态机 + 快照
  ▼                                                           │ 推送
手机 ←── HTTP GET /snapshot + WebSocket /ws ←────────────────┘
  │ POST /decision（allow/deny/alwaysAllow/allowSession）
  ▼
守护进程 ──HTTP 响应──▶ hook 脚本 ──stdout JSON──▶ CLI 继续/拒绝
```

- 全部本地 HTTP，无云端、无账号。手机与电脑在同一局域网内用 bearer-token 配对。
- 线协议见 [PROTOCOL.md](PROTOCOL.md)，与 Mac 版完全兼容；iPhone App 零改动。

## 快速开始

1. **运行守护进程**：双击 `vibebuddy-windows.exe`。首次运行会在
   `%APPDATA%\vibebuddy\` 生成 bearer token。开机自启：按 `Win+R` 输入
   `shell:startup`，把 exe 的快捷方式放进去；或用任务计划程序
   （Task Scheduler）创建一个登录时触发的任务。
2. **安装 hooks**（管理员 PowerShell 可选）：
   ```powershell
   powershell -ExecutionPolicy Bypass -File hooks\Install-VibeBuddyHooks.ps1 -Approval -Statusline
   ```
   支持 `-Agent claude|codex|grok|cursor` 只装某一个，`-Uninstall` 完整回滚。
3. **配对 iPhone**：浏览器打开 http://127.0.0.1:9876/pair ，用手机 App
   「Scan to pair」扫二维码。手表经 iPhone 自动中转，无需额外设置。
4. **用起来**：在 Claude Code / Codex / Grok / Cursor 里跑任务，
   任务状态、待审批、用量都会出现在手机和手表上。

## 仓库结构

| 路径 | 说明 |
|---|---|
| `cmd/vibebuddy/` | 守护进程入口（Go，编译为单个 .exe） |
| `internal/` | HTTP 服务、状态机、配对、快照/WebSocket（Go） |
| `hooks/` | PowerShell 版 hook 脚本 + 安装器 |
| `web/` | 本地仪表盘（任务收件箱、配对二维码） |
| `PROTOCOL.md` | iPhone ↔ 电脑线协议文档 |

## 已知限制

- 手机 App 完全关掉后的远程推送需要苹果 APNs 证书——这是上游公开版同样没解决
  的问题；App 开着或同局域网时实时性不受影响。
- Mac 专属能力（菜单栏/刘海小组件、AppleScript 终端定位、Sparkle 自动更新）
  在 Windows 版中分别由本地网页仪表盘、Win32 窗口枚举、GitHub Releases 代替。
- `capture-terminal.ps1`（跳回终端的 Win32 版）与 `cursor-followup` 仍在路线图上。

## 致谢与协议

本项目是 `semantic-craft/iOS-vibebuddy`（MIT）的 Windows 移植，
协议与 hook 语义忠实复刻上游实现。iPhone / Apple Watch 客户端请使用上游的
App Store 版本。**独立社区项目，与 Anthropic、OpenAI、Apple 无关。**

MIT License，见 [LICENSE](LICENSE)。
