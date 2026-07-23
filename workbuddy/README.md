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
- **签到**：CN 09:00 / 21:00 + 面板手动；多标签 per-account 锁；**Global 不定时领 trial**
- **积分生命周期**：CN 耗尽自动 `disabled`；有积分/签到后再开；Global 耗尽**删除** auth 文件
- **积分面板**：耗尽/禁用角标、进度条、CN/Global 筛选、导入凭证、防 management key IP 封禁
- **Scheduler**（可选）：`scheduler_mode: off|credits`（**默认 off**）；credits 优先非耗尽账号
- **OAuth 别名/排除**：由 CPA 宿主 `oauth-model-alias` / `oauth-excluded-models` 处理

### 安装

**推荐：从 GitHub Release 安装多架构包**（符合 CPA 插件商店 `ArchiveName`）：

```bash
# linux/amd64（x86_64 服务器）
unzip workbuddy_0.6.0_linux_amd64.zip   # → workbuddy.so
cp workbuddy.so /path/to/cliproxyapi/plugins/workbuddy.so
```

也可放在平台子目录：`plugins/linux/amd64/`、`plugins/darwin/arm64/` 等。

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy:
      enabled: true
      # checkin_auto: true      # CN 定时签到
      # lifecycle_auto: true    # 耗尽关/删、回血再开
      # scheduler_mode: off     # or credits
```

重启 CPA。

### 登录 / 凭证

1. CPA 管理端 OAuth 选择 WorkBuddy 完成授权；或  
2. 面板「导入凭证」弹窗粘贴 JSON → `POST .../import`  
3. 落盘：`auths/workbuddy-<uid>.json`（含 `type`/`note`/`disabled` + nested auth）

### 生命周期规则

| 区域 | 积分耗尽 | 之后 |
|------|----------|------|
| CN | 写 `disabled:true` | 签到/刷新后有积分 → 再打开 |
| Global | **删除文件** | 需重新登录/导入 |

Auth 页 CPAMP 备注行显示 `note`（区域+积分摘要）。筛选图标「W」属 CPAMP 前端静态表，**插件无法改成 logo**；完整管理用侧栏 WorkBuddy 面板。

### 预期模型

`/v1/models` 中 `owned_by=workbuddy` 的动态列表（账号权限为准），常见：`deepseek-v4-flash` / `deepseek-v4-pro` / `glm-5.x` / `kimi-k2.7` / `hy3*` / `minimax-m3` 等；可用 `oauth-model-alias` / `oauth-excluded-models` 管理。

### CPAMP / 远程更新

- 源码仓：`https://github.com/Sliverkiss/cpa-plugin`
- **商店源（registry）**：`https://raw.githubusercontent.com/Sliverkiss/cpa-plugin/main/registry.json`
- Release 资产：`workbuddy_<ver>_<goos>_<goarch>.zip` + `checksums.txt`
- 侧栏：`/v0/resource/plugins/workbuddy/panel`

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

Download the matching zip from [Releases](https://github.com/Sliverkiss/cpa-plugin/releases):

```text
workbuddy_<version>_linux_amd64.zip   # workbuddy.so
workbuddy_<version>_linux_arm64.zip
workbuddy_<version>_darwin_arm64.zip  # workbuddy.dylib
workbuddy_<version>_windows_amd64.zip # workbuddy.dll
```

Unzip and place the library in CPA `plugins/` (or `plugins/<goos>/<goarch>/`). Enable:

```yaml
plugins:
  configs:
    workbuddy:
      enabled: true
```

### Remote update

Add custom plugin source:

```text
https://raw.githubusercontent.com/Sliverkiss/cpa-plugin/main/registry.json
```

### Build

```bash
make test && make vet && make build VERSION=$(cat VERSION)
```

Release artifacts are produced by `.github/workflows/build.yml` for linux/darwin/windows/freebsd multi-arch.

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
