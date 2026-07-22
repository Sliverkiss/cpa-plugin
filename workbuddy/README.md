# WorkBuddy Plugin / WorkBuddy 插件

[English](#english) | [中文](#中文)

Tencent **CodeBuddy** (`copilot.tencent.com`) provider plugin for [CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI).

---

## 中文

### 功能

- **OAuth 登录**：多账号 `workbuddy-<uid>.json`
- **动态模型**：上游 models API + 5min 缓存 + 硬编码 fallback
- **Executor**：流/非流 SSE 聚合、cleanChunk、跨协议 framing、alias 反解、tool_choice 归一
- **Usage 上报**：`usage.PublishRecord` 三出口
- **签到**：09:00 / 21:00 + 面板手动；多标签 per-account 锁
- **积分面板**：耗尽角标、进度条、导入凭证 JSON
- **Scheduler**（可选）：`scheduler_mode: off|credits`（**默认 off**）
- **OAuth 别名/排除**：由 CPA 宿主 `oauth-model-alias` / `oauth-excluded-models` 处理

### 安装

```bash
# 产物命名（GitHub Release / 手工）
# workbuddy_linux_arm64.so  /  workbuddy_linux_amd64.so
cp workbuddy_linux_arm64.so /path/to/cliproxyapi/plugins/workbuddy.so
```

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy:
      enabled: true
      # checkin_auto: true
      # scheduler_mode: off   # or credits
```

重启 CPA。仅替换 `plugins/workbuddy.so`，**不要改 CPA / CPAMP 源码**。

### CPAMP / Plugins Store

- 仓库：`https://github.com/Sliverkiss/cpa-plugin`（`GitHubRepository` 元数据）
- 安装：CPAMP 插件商店选 Release，或拷贝上表 so 到 `plugins.dir`
- 资产约定：`workbuddy_<os>_<arch>.so`（见 `.github/workflows/release.yml`）
- 侧栏：资源页 `/v0/resource/plugins/workbuddy/panel`
- OAuth：管理端登录页使用插件 Logo

### 构建与测试

```bash
cd workbuddy
make test && make vet && make build VERSION=$(cat VERSION)
# dist/workbuddy.so
```

### 管理 API

| 路径 | 方法 | 说明 |
|---|---|---|
| `.../accounts` | GET | 账号 + credits + **exhausted** |
| `.../refresh` | POST | 强制刷新缓存 |
| `.../checkin` | POST | 手动签到 |
| `.../checkin/config` | POST | 自动签到开关 |
| `.../credits` | GET | 实时积分 |
| `.../import` | POST | 导入凭证 `{"json":{...}}` 或 `{"raw":"..."}` |

面板：`/v0/resource/plugins/workbuddy/panel`

### 已知策略

- **hy3\*** 系列：executor 将 `reasoning_effort` 钉为 `high`（非 ThinkingApplier 能力；见源码 `forceMaxThinking`）
- **count_tokens**：上游无 API，返回 `{"input_tokens":0}`
- **checkin_auto / scheduler_mode**：config_yaml 可配；面板 checkin 开关运行时不写回 yaml
- **host.http.do**：评估结论见 `docs/host-http-evaluation.md`（暂不迁移）
- **包结构**：同包多文件，不拆 internal（`docs/package-layout.md`）

### 文件

| 文件 | 说明 |
|---|---|
| `main.go` | ABI / OAuth / executor |
| `management.go` | 面板 / 签到 / 导入 |
| `scheduler.go` | scheduler.pick |
| `panel.html` | 前端 |
| `LICENSE` / `VERSION` / `CHANGELOG.md` | 发布元数据 |
| `.github/workflows/release.yml` | 多架构 Release |

---

## English

### Features

OAuth multi-account provider, dynamic models, production executor (SSE, tools, aliases), usage reporting, daily check-in, credits dashboard with **exhausted** badge, optional **credits scheduler** (`scheduler_mode`, default `off`), credential JSON import.

### Install

Copy the platform `.so` to CPA `plugins/` as `workbuddy.so`, enable under `plugins.configs.workbuddy`, restart. Do **not** patch CPA or CPA-Manager-Plus sources.

### Build

```bash
make test && make vet && make build VERSION=$(cat VERSION)
```

Release artifacts: `workbuddy_linux_arm64.so`, `workbuddy_linux_amd64.so` via GitHub Actions on tags `v*`.

### Config

```yaml
plugins:
  configs:
    workbuddy:
      enabled: true
      scheduler_mode: off   # or credits
      checkin_auto: true
```

### Notes

- hy3\* models force `reasoning_effort=high` in-plugin
- `count_tokens` stub returns zero input tokens
- See `docs/host-http-evaluation.md` and `docs/package-layout.md`
