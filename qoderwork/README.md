# QoderWork CPA 插件

[CLIProxyAPI (CPA)](https://github.com/router-for-me/CLIProxyAPI) 的 QoderWork（qoder.com.cn，CN 区）Provider 插件：多账号 OAuth/PAT 双登录、动态模型、COSY 签名推理、每日签到、积分面板、token 自动保活。

## 功能

| 能力 | 说明 |
|---|---|
| **双登录方式** | ① OAuth 设备授权（PKCE，浏览器授权，dt- 30 天 + drt- 1 年自动旋转）② PAT 导入（pt-，长期有效兜底）——两家族可共存于同一 auth 文件 |
| **登录自动领包** | OAuth 登录成功后自动判断并领取一次性 Pro 升级包（eligibility → claim） |
| **COSY 推理** | RSA 包 AES 会话密钥 + MD5 请求签名，对接 gateway.qoder.com.cn SSE 流式 |
| **动态模型** | COSY 拉取 `/algo/api/v2/model/list`（chat scene），10 静态模型兜底 |
| **每日签到** | 面板手动签到（单账号/批量）+ 09:00/21:00 定时自动签到，签到后返回最新积分快照 |
| **积分面板** | 账号卡片：昵称/积分/计划/签到状态/操作（签到/刷新/选用） |
| **token 保活** | 22:00 定时刷新；按 token 前缀路由（drt- → deviceToken/refresh，jrt- → jobToken/refresh），PAT 永不劫持 OAuth 刷新 |
| **auth 隔离** | 文件名前缀 `qoderwork-` 过滤，与 workbuddy 等其他插件互不干扰 |

## 安装

### 从 Release（推荐）

```bash
# 按你的平台下载（示例 linux/arm64）
unzip qoderwork_0.2.6_linux_arm64.zip
cp qoderwork.so /path/to/cliproxyapi/plugins/qoderwork.so
```

### 从源码

```bash
cd qoderwork
CGO_ENABLED=1 go build -buildmode=c-shared -ldflags "-X main.version=0.2.6" -o qoderwork.so .
cp qoderwork.so /path/to/cliproxyapi/plugins/
```

### config.yaml

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    qoderwork:
      enabled: true
      # cn (default): qoder.com.cn OAuth/PAT + direct COSY transport
      # global: qoder.com account through an authenticated local qodercli
      backend: cn

# 模型别名（可选）
oauth-model-alias:
  qoderwork:
    - name: qmodel_preview
      alias: qoder/qwen3.8-max
    - name: qmodel_latest
      alias: qoder/qwen3.7-max
```

### China and global Qoder are different services

`backend: cn` preserves the existing plugin behavior. It signs requests
directly for `qoder.com.cn`, manages CN device/PAT tokens, and supports the
CN-only credits and daily check-in APIs.

`backend: global` does not reuse or send those CN credentials. It delegates
authentication, token refresh, model availability validation, and signed inference to the
official `qodercli` for `qoder.com`/`qoder.sh`. Install and authenticate it
first:

```bash
brew install qoderai/qoder/qodercli --cask
qodercli login
qodercli --list-models
```

Then set:

```yaml
plugins:
  configs:
    qoderwork:
      enabled: true
      backend: global
      cli_path: qodercli
```

The global backend intentionally does not call the China OAuth, credits, or
check-in endpoints.

## 使用

### 方式一：OAuth 登录（推荐）

1. CPA 管理面板 → Auth 文件 → QoderWork OAuth 登录卡片
2. 浏览器打开授权链接 → 登录 qoder.com.cn（阿里云 SSO）→ 点 Continue 授权
3. 插件自动轮询拿 token 落盘（dt-/drt-），并自动领取 Pro 升级包（若 eligible）

### 方式二：PAT 导入

1. qoder.com.cn → 设置 → Personal Access Token → 创建（pt- 开头）
2. 插件面板（`/v0/resource/plugins/qoderwork/panel`）→ 右上角「导入 QoderWork 凭证」→ 粘贴 PAT → 导入
3. 插件自动换 jobToken（jt-/jrt-）落盘

### 面板

`/v0/resource/plugins/qoderwork/panel`（需 management key）

- 账号卡片：积分余额 / 计划 / 签到按钮（签到后返回最新积分）
- 「全部签到」批量签到；自动签到开关（09:00/21:00）

## 凭证家族与刷新

```
auth 文件字段（可共存）：
  accessToken:   dt- (OAuth, ~30d) 或 jt- (PAT 交换, 24h)
  refreshToken:  drt- (~1y, 旋转)   或 jrt- (48h)
  personalToken: pt- (长期兜底，导入即永久)
```

- 刷新路由按 `refreshToken` 前缀：`drt-` → `deviceToken/refresh`；`jrt-` → `jobToken/refresh` → 失败 fallback PAT re-exchange
- `personalToken` 永不主动覆盖活跃 token，只做最终兜底
- host 15 分钟 auto-refresh + 插件 22:00 keepalive 均按此规则

## 模型

10 个静态模型（`qmodel_preview` 等）+ COSY 动态拉取。CPA 侧别名示例：`qoder/qwen3.8-max` → `qmodel_preview`。

## 参考文档

- [KNOWLEDGE.md](../KNOWLEDGE.md) — QoderWork API 逆向全记录（COSY 签名/编码/端点）
- [analysis/api-endpoints-scan.md](../analysis/api-endpoints-scan.md) — 客户端全端点扫描
- [analysis/qoderwork-real-oauth.md](../analysis/qoderwork-real-oauth.md) — OAuth 设备授权流程 + 本地测试证据

## License

MIT（based on workbuddy by lovingfish，见 [LICENSE](LICENSE)）
