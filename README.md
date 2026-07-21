# CPA 插件仓库

本仓库收集并维护自用的 [CPA](https://github.com/router-for-me/CLIProxyAPI)（CLIProxyAPI）插件，主要用于扩展 CPA 的 OAuth 提供商、模型列表与管理面板能力。

## 仓库结构

```
workbuddy/      # WorkBuddy / CodeBuddy 插件：Tencent CodeBuddy OAuth 登录、动态模型拉取、签到、积分查询
```

## 插件列表

| 插件 | 说明 | 构建产物 |
|---|---|---|
| [workbuddy](workbuddy/) | Tencent CodeBuddy  provider，支持 OAuth 登录、模型动态发现、每日自动签到、积分与套餐面板 | `workbuddy.so` |

## 快速使用

每个插件目录下都有独立的 `README.md` 说明文件，进入对应目录查看详细安装与使用方法。

## 许可证

自用仓库，按各插件内部声明的许可证使用。
