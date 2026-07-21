# WorkBuddy 插件

WorkBuddy（CodeBuddy）CPA 插件，基于 Tencent CodeBuddy（原 WorkBuddy）OAuth 流程，支持动态模型拉取、每日自动签到、积分与套餐面板展示。

## 功能特性

- **OAuth 登录**：通过 CodeBuddy 网页扫码/授权流程获取 access token。
- **动态模型拉取**：登录后自动从 `copilot.tencent.com` 拉取可用模型列表，无需硬编码。
- **每日自动签到**：支持每日 09:00 和 21:00 自动签到领取积分。
- **积分与套餐面板**：在 CPA 管理面板查看账号昵称、积分余额、套餐余量、用量进度、签到状态。
- **OAuth 模型别名/禁用**：由 CPA 原生 `oauth-model-alias` / `oauth-excluded-models` 管理。

## 安装方法

1. 将构建产物 `workbuddy.so` 复制到 CPA 的插件目录：

```bash
cp workbuddy.so /path/to/cliproxyapi/plugins/workbuddy.so
```

2. 在 CPA `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    workbuddy:
      enabled: true
```

3. 重启 CPA 服务。

## 构建方法

需要 Go 1.26.0+ 环境：

```bash
cd workbuddy
export PATH=$PATH:/usr/local/go/bin
go build -buildmode=c-shared -o workbuddy.so .
```

## 配置说明

### 自动签到

插件默认启用自动签到。可通过管理面板开关，或调用管理 API：

```bash
curl -X POST -H "Authorization: Bearer $CPA_MGMT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"enabled":true}' \
  http://127.0.0.1:12888/v0/management/plugins/workbuddy/checkin/config
```

### OAuth 模型别名 / 禁用

在 CPA `config.yaml` 中配置：

```yaml
oauth-excluded-models:
  workbuddy:
    - hy3
    - minimax-m3

oauth-model-alias:
  workbuddy:
    - name: deepseek-v4-pro
      alias: workbuddy-dsv4-pro
      fork: true
```

## 管理面板

插件注册了一个 Web 面板：

```
http://<cpa-host>/v0/resource/plugins/workbuddy/panel
```

面板功能：
- 查看所有 WorkBuddy 账号的积分、套餐、用量进度
- 手动签到 / 全部签到
- 开启/关闭自动签到

## 管理 API

| 接口 | 方法 | 说明 |
|---|---|---|
| `/v0/management/plugins/workbuddy/accounts` | GET | 列出账号、积分、签到状态 |
| `/v0/management/plugins/workbuddy/checkin` | POST | 单个/全部签到 |
| `/v0/management/plugins/workbuddy/checkin/config` | POST | 设置自动签到开关 |

## 文件说明

- `main.go`：插件主入口，OAuth 登录/刷新、模型动态拉取、executor 转发。
- `management.go`：管理 API 与自动签到调度。
- `panel.html`：管理面板前端页面。
- `go.mod` / `go.sum`：Go 模块依赖。
- `workbuddy.so`：预编译的插件二进制。

## 注意事项

- 插件需要 CPA 管理密钥才能访问管理 API；从 CPA 主面板嵌入时会自动读取 localStorage 中的密钥。
- 多个 WorkBuddy 账号登录时，插件会按 `workbuddy-<uid>.json` 命名保存，避免互相覆盖。
- 模型列表通过 `GET /console/enterprises/personal/models` 动态获取，需要账号已登录且 token 有效。
