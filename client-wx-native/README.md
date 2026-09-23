# WeChat Native Client

该目录是客户端方案 B：使用微信官方原生小程序 UI、生命周期和 API 的独立实现，后续作为 KMP MiniApp 方案的工程对照 baseline。

## Current Status

业务页面开发已启动：包含实时监控（dashboard）、历史趋势（trends）、告警记录（alerts）、阈值设置（settings）四个 tab 页，以及公共网络层（services/）与工具函数（utils/）。底部导航图标位于 assets/icons/。

后端全部路由已实现（2026-09-22，见 `backend/README.md`）。当前通过 `config/env.js` 的 `useMock` 开关使用本地 Mock 数据（services/mock/），Mock 响应字段与 api 契约一致；联调时将 `useMock` 置为 `false` 即可切换真实接口，页面代码无需改动。原示例页（index/logs）已移除。

## Project Boundary

- 后续与 KMP 客户端实现相同业务能力，并使用相同 Backend API、硬件数据源和验收场景。
- 保持微信原生工程方式和真实开发成本，不为了匹配 KMP 目录结构而人为改造。
- 不依赖 `client-kmp` 的内部实现；跨客户端只共享已确认的外部契约和需求事实。
- WebSocket 实时订阅已实现（`services/socket.js`：Envelope 解析、eventId 去重、断线重连与 REST 兜底）；ECharts 折线图与单元测试在后续阶段补齐。

使用微信开发者工具打开本目录即可运行。`project.config.json` 中已有项目配置与团队确认的 AppID；`project.private.config.json` 等本机私有文件不得提交。

## Collaboration Workflow

```sh
git clone <repository-url>
cd final
git switch main
git pull --ff-only
git switch -c feat/wx-monitor-dashboard

# 在微信开发者工具中完成编译、模拟器和必要的真机验证
git add client-wx-native
git commit -m "feat(wx): add monitor dashboard"
git push -u origin feat/wx-monitor-dashboard
```

分支必须采用 `<type>/wx-<complete-description>`，例如 `fix/wx-threshold-validation`。PR 关联 Multica issue，说明页面/组件、微信 API、Backend 契约和开发者工具/真机验证结果；经审核和检查通过后方可合并。`project.private.config.json` 等本机私有文件不得提交。
